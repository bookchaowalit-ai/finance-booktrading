"""SELL executions consume the lots they sold and realize their PnL, whether
the order filled or was cancelled after a partial fill."""

import time
from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest

import app.real_grid_bot as real_grid_bot
from app.real_grid_bot import RealGridBot, RealGridConfig, RealGridState
from app.risk_manager import RiskManager

FEE = real_grid_bot.BINANCE_TH_FEE_PCT / 100.0


def _response(status, payload=None):
    return httpx.Response(status, json=payload or {}, request=httpx.Request("GET", "http://backend"))


def _net(sell, pieces):
    """Expected net PnL of selling the (buy_price, qty) pieces at sell."""
    return sum((sell - b) * q - b * q * FEE - sell * q * FEE for b, q in pieces)


def _bot(monkeypatch, state, open_orders=(), final_executed=None, cancel_ok=True):
    cfg = RealGridConfig(symbol="BTCTHB", order_size=1.0, buy_only=False, stop_loss_pct=3.0)
    bot = RealGridBot(configs=[cfg])
    bot.states = {"BTCTHB": state}
    bot._journal = MagicMock(record_exit=AsyncMock())
    bot._save_state = AsyncMock()
    bot._webhook = MagicMock(send_fill_alert=AsyncMock())
    risk = RiskManager()
    risk.record_trade_result = MagicMock()
    bot._risk = risk
    monkeypatch.setattr(real_grid_bot, "get_risk_manager", lambda: risk)
    final_executed = final_executed or {}
    posted = []

    async def post(url, json=None):
        posted.append((url, json))
        if url.endswith("/api/trade/cancel-order") and not cancel_ok:
            return _response(502)
        return _response(200)

    async def get(url, params=None):
        if url.endswith("/api/bot/status"):
            return _response(200, {"is_active": False})
        if url.endswith("/api/trade/order-status"):
            return _response(200, {"status": "CANCELED", "executedQty": str(final_executed.get(int(params["orderId"]), 0))})
        return _response(200, {"orders": list(open_orders)})

    bot._http = MagicMock(post=AsyncMock(side_effect=post), get=AsyncMock(side_effect=get))
    return bot, cfg, posted


def _lots(**lots):
    """State holding lots {buy_price: qty}, filled in the given order."""
    now = time.time()
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0)
    for i, (price, qty) in enumerate(lots.items()):
        p = float(price.lstrip("p"))
        state.filled_buy_prices[p] = 0
        state.buy_fill_prices[p] = now - 100 + i
        state.lot_qty[p] = qty
    return state


@pytest.mark.asyncio
async def test_filled_sell_consumes_every_lot_it_sold(monkeypatch):
    """A 1.0 sell over lots of 0.4 @100 and 0.6 @90 closes both, PnL per entry."""
    state = _lots(p100=0.4, p90=0.6)
    state.active_sells = {110.0: "5"}
    bot, cfg, _ = _bot(monkeypatch, state, final_executed={5: 1.0})
    bot._verify_fill = AsyncMock(return_value="filled")

    await bot._sync_open_orders(cfg, state)

    assert state.filled_buy_prices == {} and state.lot_qty == {}
    assert state.cumulative_pnl == pytest.approx(_net(110.0, [(100.0, 0.4), (90.0, 0.6)]))


@pytest.mark.asyncio
async def test_kill_books_pnl_of_a_partly_filled_sell(monkeypatch):
    state = _lots(p100=1.0)
    state.active_sells = {110.0: "5"}
    bot, _, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 5, "side": "SELL"}], final_executed={5: 0.3})

    result = await bot.kill()

    assert result["contained"] is True
    assert state.lot_qty[100.0] == pytest.approx(0.7)
    expected = _net(110.0, [(100.0, 0.3)])
    assert state.cumulative_pnl == pytest.approx(expected)
    assert state.daily_pnl == pytest.approx(expected)
    bot._risk.record_trade_result.assert_called_once()
    assert bot._risk.record_trade_result.call_args.args[1] == pytest.approx(expected)
    oid, _, _, pnl, _ = bot._journal.record_exit.await_args.args
    assert oid == "5" and pnl == pytest.approx(expected)


@pytest.mark.asyncio
async def test_partly_filled_stop_loss_loss_is_booked(monkeypatch):
    """A cancelled partial stop-loss realizes its loss, then re-arms the rest."""
    state = _lots(p100=1.0)
    state.stop_loss_triggered = {100.0: True}
    state.stop_loss_orders = {100.0: 96.0}
    state.active_sells = {96.0: "7"}
    bot, _cfg, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 7, "side": "SELL"}], final_executed={7: 0.4})

    await bot.kill()

    assert state.cumulative_pnl == pytest.approx(_net(96.0, [(100.0, 0.4)]))
    assert state.cumulative_pnl < 0
    assert state.lot_qty[100.0] == pytest.approx(0.6)
    assert state.stop_loss_triggered[100.0] is False


@pytest.mark.asyncio
async def test_sell_execution_is_booked_once(monkeypatch):
    state = _lots(p100=1.0)
    bot, _, _ = _bot(monkeypatch, state)

    await bot._realize_sell(state, 110.0, 0.3, "5", "PARTIAL_SELL_CANCELLED")
    await bot._realize_sell(state, 110.0, 0.3, "5", "PARTIAL_SELL_CANCELLED")

    assert state.lot_qty[100.0] == pytest.approx(0.7)
    assert state.cumulative_pnl == pytest.approx(_net(110.0, [(100.0, 0.3)]))
    bot._risk.record_trade_result.assert_called_once()


@pytest.mark.asyncio
async def test_kill_books_cancelled_buys_before_sells(monkeypatch):
    """Exchange lists the SELL first; the BUY's lot must exist before the SELL
    shrinks it (0.5 bought, 0.2 sold leaves 0.3)."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"}, active_sells={110.0: "12"})
    bot, _, _ = _bot(
        monkeypatch, state,
        open_orders=[{"orderId": 12, "side": "SELL"}, {"orderId": 11, "side": "BUY"}],
        final_executed={11: 0.5, 12: 0.2},
    )

    await bot.kill()

    assert state.lot_qty[100.0] == pytest.approx(0.3)
    assert state.cumulative_pnl == pytest.approx(_net(110.0, [(100.0, 0.2)]))


def _lot_with_live_stop():
    state = _lots(p100=1.0)
    state.stop_loss_triggered = {100.0: True}
    state.stop_loss_orders = {100.0: 96.0}
    state.active_sells = {96.0: "7", 110.0: "5"}
    return state


@pytest.mark.asyncio
async def test_partial_grid_sell_resizes_the_live_stop(monkeypatch):
    """A grid sell closing part of a lot whose stop rests on the book cancels
    that stop and re-arms it for the remainder, never a second stop beside it."""
    state = _lot_with_live_stop()
    bot, cfg, posted = _bot(monkeypatch, state, open_orders=[{"orderId": 7, "status": "NEW"}], final_executed={5: 0.4, 7: 0})

    async def verify(symbol, oid):
        return "filled" if oid == "5" else "open"

    bot._verify_fill = AsyncMock(side_effect=verify)

    await bot._sync_open_orders(cfg, state)

    cancelled = [b["orderId"] for u, b in posted if u.endswith("/api/trade/cancel-order")]
    assert cancelled == [7]
    assert 96.0 not in state.active_sells
    assert state.lot_qty[100.0] == pytest.approx(0.6)
    assert state.stop_loss_triggered[100.0] is False  # re-armed for 0.6


@pytest.mark.asyncio
async def test_partial_grid_sell_keeps_stop_when_cancel_fails(monkeypatch):
    state = _lot_with_live_stop()
    bot, cfg, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 7, "status": "NEW"}], final_executed={5: 0.4}, cancel_ok=False)

    async def verify(symbol, oid):
        return "filled" if oid == "5" else "open"

    bot._verify_fill = AsyncMock(side_effect=verify)
    bot._place_grid_order = AsyncMock(return_value=True)

    await bot._sync_open_orders(cfg, state)
    await bot._check_stop_loss(cfg, state, 95.5)  # below the stop threshold, above the chase

    # The old stop is still live and still tracked: no second stop.
    assert state.active_sells.get(96.0) == "7"
    assert state.stop_loss_orders[100.0] == 96.0
    bot._place_grid_order.assert_not_awaited()


def _journal_exits(bot, order_id):
    return [c.args for c in bot._journal.record_exit.await_args_list if c.args[0] == order_id]


@pytest.mark.asyncio
async def test_superseded_stop_is_not_reimported_from_the_snapshot(monkeypatch):
    state = _lot_with_live_stop()
    snapshot = [{"orderId": 7, "status": "NEW", "side": "SELL", "price": "96"}]
    bot, cfg, _ = _bot(monkeypatch, state, open_orders=snapshot, final_executed={5: 0.4, 7: 0.1})

    async def verify(symbol, oid):
        return "filled" if oid == "5" else "open"

    bot._verify_fill = AsyncMock(side_effect=verify)

    await bot._sync_open_orders(cfg, state)

    assert 96.0 not in state.active_sells
    # The stop's journal entry is closed once, with its partial-fill loss.
    (exit_args,) = _journal_exits(bot, "7")
    assert exit_args[2] == "PARTIAL_SELL_CANCELLED"
    assert exit_args[3] == pytest.approx(_net(96.0, [(100.0, 0.1)]))


@pytest.mark.asyncio
async def test_stale_partial_sell_journal_closed_once_with_pnl(monkeypatch):
    """The backend journal only updates OPEN entries: a CANCELLED exit written
    first would swallow the realized PnL."""
    state = _lots(p100=1.0)
    state.active_sells = {110.0: "9"}
    bot, cfg, _ = _bot(monkeypatch, state, final_executed={9: 0.3})
    state.order_times["9"] = time.time() - bot.MAX_ORDER_AGE_SECONDS - 1

    await bot._cancel_stale_orders(cfg, state, 105.0)

    (exit_args,) = _journal_exits(bot, "9")
    assert exit_args[2] == "PARTIAL_SELL_CANCELLED"
    assert exit_args[3] == pytest.approx(_net(110.0, [(100.0, 0.3)]))


@pytest.mark.asyncio
async def test_unfilled_cancelled_sell_is_journalled_cancelled_once(monkeypatch):
    state = _lots(p100=1.0)
    state.active_sells = {110.0: "9"}
    bot, cfg, _ = _bot(monkeypatch, state, final_executed={9: 0})
    state.order_times["9"] = time.time() - bot.MAX_ORDER_AGE_SECONDS - 1

    await bot._cancel_stale_orders(cfg, state, 105.0)

    assert _journal_exits(bot, "9") == [("9", 0.0, "CANCELLED", 0.0)]


@pytest.mark.asyncio
async def test_cancelled_partial_grid_sell_resizes_the_live_stop(monkeypatch):
    """A stale grid SELL cancelled after selling 0.4 of a 1.0 lot leaves a
    stop sized for 1.0: it is cancelled and re-armed for 0.6."""
    state = _lot_with_live_stop()
    bot, cfg, posted = _bot(monkeypatch, state, final_executed={5: 0.4, 7: 0})
    state.order_times["5"] = time.time() - bot.MAX_ORDER_AGE_SECONDS - 1
    state.order_times["7"] = time.time()

    await bot._cancel_stale_orders(cfg, state, 105.0)

    cancelled = [b["orderId"] for u, b in posted if u.endswith("/api/trade/cancel-order")]
    assert cancelled == [5, 7]
    assert state.active_sells == {}
    assert state.lot_qty[100.0] == pytest.approx(0.6)
    assert state.stop_loss_triggered[100.0] is False


@pytest.mark.asyncio
async def test_external_cancel_superseded_stop_is_not_reimported(monkeypatch):
    state = _lot_with_live_stop()
    snapshot = [{"orderId": 7, "status": "NEW", "side": "SELL", "price": "96"}]
    bot, cfg, _ = _bot(monkeypatch, state, open_orders=snapshot, final_executed={5: 0.4, 7: 0})
    bot._verify_fill = AsyncMock(side_effect=lambda symbol, oid: "cancelled" if oid == "5" else "open")

    await bot._sync_open_orders(cfg, state)

    assert state.active_sells == {}
    assert state.lot_qty[100.0] == pytest.approx(0.6)


@pytest.mark.asyncio
async def test_halted_reconcile_survives_a_stop_removed_mid_iteration(monkeypatch):
    """The partial grid SELL comes first and cancels the stop the loop has
    not reached yet; reconciliation must still finish and save."""
    state = _lot_with_live_stop()
    state.active_sells = {110.0: "5", 96.0: "7"}
    bot, cfg, _ = _bot(monkeypatch, state, final_executed={5: 0.4, 7: 0})
    bot._verify_fill = AsyncMock(return_value="cancelled")

    await bot._reconcile_halted_orders(cfg, state)

    assert state.active_sells == {}
    bot._save_state.assert_awaited()


@pytest.mark.asyncio
async def test_kill_books_partial_fill_of_an_untracked_buy(monkeypatch):
    """An order the bot never tracked (its POST timed out) shows up in the
    drain partly filled: its fill becomes a protected lot."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0)
    bot, _, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 99, "side": "BUY", "price": "100"}], final_executed={99: 0.3})

    result = await bot.kill()

    assert result["contained"] is True
    assert state.lot_qty[100.0] == pytest.approx(0.3)
    assert 100.0 in state.filled_buy_prices
    assert _journal_exits(bot, "99") == [("99", 100.0, "FILLED_BUY", 0.0)]


@pytest.mark.asyncio
async def test_kill_books_pnl_of_an_untracked_partial_sell(monkeypatch):
    state = _lots(p100=1.0)
    bot, _, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 98, "side": "SELL", "price": "110"}], final_executed={98: 0.2})

    await bot.kill()

    assert state.lot_qty[100.0] == pytest.approx(0.8)
    assert state.cumulative_pnl == pytest.approx(_net(110.0, [(100.0, 0.2)]))


@pytest.mark.asyncio
async def test_untracked_partial_fill_without_side_or_price_is_not_contained(monkeypatch):
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0)
    bot, _, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 97}], final_executed={97: 0.1})

    result = await bot.kill()

    assert result["contained"] is False
    assert any(u["orderId"] == 97 for u in result["unreconciled"])


@pytest.mark.asyncio
async def test_kill_closes_journal_of_a_partly_filled_tracked_buy(monkeypatch):
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    bot, _, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 11, "side": "BUY", "price": "100"}], final_executed={11: 0.4})

    await bot.kill()

    assert state.lot_qty[100.0] == pytest.approx(0.4)
    assert _journal_exits(bot, "11") == [("11", 100.0, "FILLED_BUY", 0.0)]


@pytest.mark.asyncio
async def test_overlapping_sync_does_not_erase_cancellation_tracking(monkeypatch):
    """A restart-triggered sync running while another sync awaits a cancelled
    stop's execution must not make the first one re-import that stop."""
    import asyncio

    state = _lot_with_live_stop()
    snapshot = [{"orderId": 7, "status": "NEW", "side": "SELL", "price": "96"}]
    bot, cfg, _ = _bot(monkeypatch, state, open_orders=snapshot)
    bot._verify_fill = AsyncMock(side_effect=lambda symbol, oid: "filled" if oid == "5" else "open")
    entered, release = asyncio.Event(), asyncio.Event()

    async def final_executed(client, symbol, oid):
        if oid == "7":
            entered.set()
            await release.wait()
            return 0.0
        return 0.4

    bot._final_executed_qty = final_executed
    first = asyncio.create_task(bot._sync_open_orders(cfg, state))
    await entered.wait()
    await bot._sync_open_orders(cfg, RealGridState(symbol="BTCTHB"))  # the overlapping sync
    release.set()
    await first

    assert 96.0 not in state.active_sells


@pytest.mark.asyncio
async def test_untracked_buy_adds_to_an_existing_implicit_size_lot(monkeypatch):
    """A lot without a recorded size holds the order size (1.0); an untracked
    0.3 fill at the same price makes it 1.3, not 0.3."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, filled_buy_prices={100.0: 0}, buy_fill_prices={100.0: time.time()})
    bot, _, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 99, "side": "BUY", "price": "100"}], final_executed={99: 0.3})

    await bot.kill()

    assert state.lot_qty[100.0] == pytest.approx(1.3)


def test_finished_sync_removes_its_own_cancel_set():
    """Two syncs' sets compare equal while empty; removal must be by identity."""
    bot = RealGridBot(configs=[RealGridConfig(symbol="BTCTHB")])
    first, second = set(), set()
    bot._sync_cancel_sets = [first, second]
    bot._end_sync_tracking(second)
    second.add("x")
    assert bot._sync_cancel_sets == [first] and bot._sync_cancel_sets[0] is first
