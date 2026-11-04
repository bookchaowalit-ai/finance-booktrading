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


def _bot(monkeypatch, state, open_orders=(), final_executed=None, cancel_ok=True, order_info=None):
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
            oid = int(params["orderId"])
            executed = final_executed.get(oid, 0)
            if executed is None:
                return _response(502)
            return _response(200, {"status": "CANCELED", "executedQty": str(executed), **(order_info or {}).get(oid, {})})
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



@pytest.mark.asyncio
async def test_unbookable_untracked_fill_stays_unreconciled_until_booked(monkeypatch):
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0)
    info = {}
    bot, _, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 97}], final_executed={97: 0.1}, order_info=info)

    assert (await bot.kill())["contained"] is False
    # The order is off the book now; a retry must still not report contained.
    get = bot._http.get.side_effect

    async def no_open_orders(url, params=None):
        if url.endswith("/api/trade/open-orders"):
            return _response(200, {"orders": []})
        return await get(url, params=params)

    bot._http.get.side_effect = no_open_orders
    assert (await bot.kill())["contained"] is False

    # Once the exchange reports side and price, the fill is booked.
    info[97] = {"side": "BUY", "price": "100"}
    assert (await bot.kill())["contained"] is True
    assert state.lot_qty[100.0] == pytest.approx(0.1)


@pytest.mark.asyncio
async def test_cancelled_buy_lot_is_booked_before_the_journal_await(monkeypatch):
    """A restart sync running while the journal write awaits may drop the
    order from active_buys; the lot must already exist by then."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    bot, _, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 11, "side": "BUY", "price": "100"}], final_executed={11: 0.4})

    async def journal_exit(*args):
        state.active_buys.clear()  # the overlapping sync

    bot._journal.record_exit = AsyncMock(side_effect=journal_exit)
    await bot.kill()

    assert state.lot_qty[100.0] == pytest.approx(0.4)



@pytest.mark.asyncio
async def test_cancelled_sell_with_unknown_execution_is_retried_by_the_kill(monkeypatch):
    """A stale stop cancelled while order-status is down leaves the book; the
    next kill must still look it up and re-arm the stop."""
    state = _lots(p100=1.0)
    state.stop_loss_triggered = {100.0: True}
    state.stop_loss_orders = {100.0: 96.0}
    state.active_sells = {96.0: "7"}
    final = {7: None}
    bot, cfg, _ = _bot(monkeypatch, state, final_executed=final)
    state.order_times["7"] = time.time() - bot.MAX_ORDER_AGE_SECONDS - 1

    await bot._cancel_stale_orders(cfg, state, 98.0)
    assert 96.0 not in state.active_sells
    assert state.stop_loss_triggered[100.0] is True  # not re-armed blind

    first = await bot.kill()
    assert first["contained"] is False

    final[7] = 0
    retry = await bot.kill()
    assert retry["contained"] is True
    assert state.stop_loss_triggered[100.0] is False


@pytest.mark.asyncio
async def test_sync_retries_cancelled_sell_with_unknown_execution(monkeypatch):
    state = _lots(p100=1.0)
    state.stop_loss_triggered = {100.0: True}
    state.stop_loss_orders = {100.0: 96.0}
    state.active_sells = {96.0: "7"}
    final = {7: None}
    bot, cfg, _ = _bot(monkeypatch, state, final_executed=final)
    state.order_times["7"] = time.time() - bot.MAX_ORDER_AGE_SECONDS - 1
    await bot._cancel_stale_orders(cfg, state, 98.0)

    final[7] = 0.4
    await bot._sync_open_orders(cfg, state)

    assert state.lot_qty[100.0] == pytest.approx(0.6)
    assert state.stop_loss_triggered[100.0] is False
    assert ("BTCTHB", "7") not in bot._unresolved_cancels


@pytest.mark.asyncio
async def test_cancelled_tracked_buy_adds_to_an_open_lot(monkeypatch):
    state = RealGridState(
        symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"},
        filled_buy_prices={100.0: 0}, buy_fill_prices={100.0: time.time()},
    )
    bot, _, _ = _bot(monkeypatch, state, open_orders=[{"orderId": 11, "side": "BUY", "price": "100"}], final_executed={11: 0.2})

    await bot.kill()

    assert state.lot_qty[100.0] == pytest.approx(1.2)


@pytest.mark.asyncio
async def test_untracked_and_tracked_buy_fills_at_one_price_add_up(monkeypatch):
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    snapshot = [{"orderId": 99, "side": "BUY", "price": "100"}, {"orderId": 11, "side": "BUY", "price": "100"}]
    bot, _, _ = _bot(monkeypatch, state, open_orders=snapshot, final_executed={99: 0.3, 11: 0.4})

    await bot.kill()

    assert state.lot_qty[100.0] == pytest.approx(0.7)


@pytest.mark.asyncio
async def test_missing_side_is_resolved_before_buys_are_booked_first(monkeypatch):
    """An untracked BUY whose side only the exchange knows is still booked
    before the SELL listed ahead of it (0.3 bought, 0.2 sold leaves 0.1)."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0)
    snapshot = [{"orderId": 98, "side": "SELL", "price": "110"}, {"orderId": 99}]
    bot, _, _ = _bot(
        monkeypatch, state, open_orders=snapshot, final_executed={98: 0.2, 99: 0.3},
        order_info={99: {"side": "BUY", "price": "100"}},
    )

    await bot.kill()

    assert state.lot_qty[100.0] == pytest.approx(0.1)
    assert state.cumulative_pnl == pytest.approx(_net(110.0, [(100.0, 0.2)]))


@pytest.mark.asyncio
async def test_unresolved_stop_keeps_its_lot_after_check_stop_loss(monkeypatch):
    """_check_stop_loss drops the stop mapping once the order left the book;
    the retry must still book against the stop's own lot and re-arm it."""
    state = _lots(p100=1.0, p120=1.0)
    state.stop_loss_triggered = {120.0: True}
    state.stop_loss_orders = {120.0: 110.0}
    state.active_sells = {110.0: "7"}
    final = {7: None}
    bot, cfg, _ = _bot(monkeypatch, state, final_executed=final)
    state.order_times["7"] = time.time() - bot.MAX_ORDER_AGE_SECONDS - 1
    bot._place_grid_order = AsyncMock(return_value=False)

    await bot._cancel_stale_orders(cfg, state, 112.0)
    await bot._check_stop_loss(cfg, state, 112.0)
    assert 120.0 not in state.stop_loss_orders  # mapping dropped meanwhile

    final[7] = 0.4
    await bot._sync_open_orders(cfg, state)

    assert state.lot_qty[120.0] == pytest.approx(0.6)  # the stop's lot, at a loss
    assert state.lot_qty[100.0] == pytest.approx(1.0)
    assert state.cumulative_pnl == pytest.approx(_net(110.0, [(120.0, 0.4)]))
    assert state.stop_loss_triggered[120.0] is False


@pytest.mark.asyncio
async def test_sync_books_buy_fills_before_retried_sells(monkeypatch):
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    bot, cfg, _ = _bot(monkeypatch, state, final_executed={9: 0.4})
    bot._unresolved_cancels[("BTCTHB", "9")] = ("SELL", 110.0, None)
    bot._verify_fill = AsyncMock(return_value="filled")

    await bot._sync_open_orders(cfg, state)

    assert state.lot_qty.get(100.0, 1.0) == pytest.approx(0.6)
    assert state.cumulative_pnl == pytest.approx(_net(110.0, [(100.0, 0.4)]))


@pytest.mark.asyncio
async def test_kill_survives_a_sync_claiming_an_unresolved_cancel(monkeypatch):
    """A restart sync can book (and remove) an unresolved cancel while the
    kill awaits another one; the kill must skip it, not crash."""
    state = _lots(p100=1.0)
    bot, _, _ = _bot(monkeypatch, state)
    bot._unresolved_cancels[("BTCTHB", "8")] = ("SELL", 110.0, None)
    bot._unresolved_cancels[("BTCTHB", "9")] = ("SELL", 111.0, None)

    async def final_executed(client, symbol, oid):
        bot._unresolved_cancels.pop(("BTCTHB", "9"), None)  # the overlapping sync
        return 0.0

    bot._final_executed_qty = final_executed
    result = await bot.kill()

    assert result["contained"] is True


def _failing_cancel(bot, fail_ids):
    """Make cancel-order fail (502) for the given order IDs."""
    post = bot._http.post.side_effect

    async def wrapped(url, json=None):
        if url.endswith("/api/trade/cancel-order") and json["orderId"] in fail_ids:
            return _response(502)
        return await post(url, json=json)

    bot._http.post.side_effect = wrapped


@pytest.mark.asyncio
async def test_failed_cancel_is_rechecked_on_retry(monkeypatch):
    """The exchange cancelled the BUY but the response was lost: the retry
    must still look it up and book its fill, not report contained blind."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    snapshot = [{"orderId": 11, "side": "BUY", "price": "100"}]
    bot, _, _ = _bot(monkeypatch, state, open_orders=snapshot, final_executed={11: 0.4})
    _failing_cancel(bot, {11})

    assert (await bot.kill())["contained"] is False
    snapshot.clear()  # it is off the book now
    retry = await bot.kill()

    assert retry["contained"] is True
    assert state.lot_qty[100.0] == pytest.approx(0.4)


@pytest.mark.asyncio
async def test_failed_cancel_still_open_is_booked_once(monkeypatch):
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    snapshot = [{"orderId": 11, "side": "BUY", "price": "100"}]
    info = {11: {"status": "NEW"}}
    bot, _, _ = _bot(monkeypatch, state, open_orders=snapshot, final_executed={11: 0.4}, order_info=info)
    failing = {11}
    _failing_cancel(bot, failing)
    await bot.kill()

    failing.clear()  # the cancel goes through this time
    info[11] = {"status": "CANCELED"}
    order_status = bot._http.get.side_effect
    calls = {"n": 0}

    async def first_open_then_cancelled(url, params=None):
        if url.endswith("/api/trade/order-status") and calls["n"] == 0:
            calls["n"] += 1
            return _response(200, {"status": "NEW", "executedQty": "0.4"})
        return await order_status(url, params=params)

    bot._http.get.side_effect = first_open_then_cancelled
    result = await bot.kill()

    assert result["contained"] is True
    assert state.lot_qty[100.0] == pytest.approx(0.4)


@pytest.mark.asyncio
async def test_sync_books_a_partly_filled_buy_cancelled_externally(monkeypatch):
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    bot, cfg, _ = _bot(monkeypatch, state, final_executed={11: 0.3})
    bot._verify_fill = AsyncMock(return_value="cancelled")

    await bot._sync_open_orders(cfg, state)

    assert state.active_buys == {}
    assert state.lot_qty[100.0] == pytest.approx(0.3)
    assert _journal_exits(bot, "11") == [("11", 100.0, "FILLED_BUY", 0.0)]


@pytest.mark.asyncio
async def test_halted_reconcile_books_a_partly_filled_cancelled_buy(monkeypatch):
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    bot, cfg, _ = _bot(monkeypatch, state, final_executed={11: 0.3})
    bot._verify_fill = AsyncMock(return_value="cancelled")

    await bot._reconcile_halted_orders(cfg, state)

    assert state.lot_qty[100.0] == pytest.approx(0.3)


@pytest.mark.asyncio
async def test_go_bot_started_during_the_drain_is_not_contained(monkeypatch):
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0)
    bot, _, _ = _bot(monkeypatch, state)
    get = bot._http.get.side_effect
    reads = {"n": 0}

    async def status_flips(url, params=None):
        if url.endswith("/api/bot/status"):
            reads["n"] += 1
            return _response(200, {"is_active": reads["n"] > 1})
        return await get(url, params=params)

    bot._http.get.side_effect = status_flips
    result = await bot.kill()

    assert result["contained"] is False
    assert any(f.get("reason") == "go_running" for f in result["failed"])


@pytest.mark.asyncio
async def test_sync_defers_sell_retry_while_a_buy_is_unresolved(monkeypatch):
    """Both cancels unresolved: the sync must not book the SELL before the
    BUY's lot exists (0.3 bought, 0.2 sold leaves 0.1)."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0)
    bot, cfg, _ = _bot(
        monkeypatch, state, final_executed={98: 0.2, 99: 0.3},
        order_info={98: {"side": "SELL", "price": "110"}, 99: {"side": "BUY", "price": "100"}},
    )
    bot._unresolved_cancels[("BTCTHB", "98")] = ("SELL", 110.0, None)
    bot._unresolved_cancels[("BTCTHB", "99")] = ("BUY", 100.0, None)

    await bot._sync_open_orders(cfg, state)
    assert ("BTCTHB", "98") in bot._unresolved_cancels  # deferred

    await bot.kill()
    assert state.lot_qty[100.0] == pytest.approx(0.1)


@pytest.mark.asyncio
async def test_cancelled_buy_fill_booked_once_across_sync_and_kill(monkeypatch):
    """Kill cancels the BUY but cannot read its fill; a sync books it; the
    next kill must not add it again (0.3 stays 0.3)."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    snapshot = [{"orderId": 11, "side": "BUY", "price": "100"}]
    final = {11: None}
    bot, cfg, _ = _bot(monkeypatch, state, open_orders=snapshot, final_executed=final)
    assert (await bot.kill())["contained"] is False

    snapshot.clear()
    final[11] = 0.3
    bot._verify_fill = AsyncMock(return_value="cancelled")
    await bot._sync_open_orders(cfg, state)
    assert state.lot_qty[100.0] == pytest.approx(0.3)

    assert (await bot.kill())["contained"] is True
    assert state.lot_qty[100.0] == pytest.approx(0.3)


@pytest.mark.asyncio
async def test_sync_does_not_book_a_live_order_whose_cancel_failed(monkeypatch):
    state = _lots(p100=1.0)
    state.stop_loss_triggered = {100.0: True}
    state.stop_loss_orders = {100.0: 96.0}
    state.active_sells = {96.0: "7"}
    snapshot = [{"orderId": 7, "side": "SELL", "price": "96", "status": "NEW"}]
    bot, cfg, _ = _bot(monkeypatch, state, open_orders=snapshot, final_executed={7: 0.1}, order_info={7: {"status": "NEW"}})
    _failing_cancel(bot, {7})
    await bot.kill()

    await bot._sync_open_orders(cfg, state)

    assert state.stop_loss_triggered[100.0] is True  # still live: no re-arm
    assert state.lot_qty.get(100.0, 1.0) == pytest.approx(1.0)
    assert ("BTCTHB", "7") in bot._unresolved_cancels


@pytest.mark.asyncio
async def test_live_unresolved_order_that_vanishes_before_the_drain_is_retried(monkeypatch):
    """Seen live by the re-check, gone (filled) before the drain lists: the
    kill is not contained, and the next one books the fill."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0)
    info = {99: {"status": "NEW"}}
    bot, _, _ = _bot(monkeypatch, state, final_executed={99: 1.0}, order_info=info)
    bot._unresolved_cancels[("BTCTHB", "99")] = ("BUY", 100.0, None)

    assert (await bot.kill())["contained"] is False
    info[99] = {"status": "FILLED"}
    assert (await bot.kill())["contained"] is True
    assert state.lot_qty[100.0] == pytest.approx(1.0)


@pytest.mark.asyncio
async def test_filled_buy_found_by_sync_is_not_booked_again_by_a_retry(monkeypatch):
    """Cancel fails, the BUY then fills, a sync books it: the next kill's
    re-check must not add the fill a second time (1.0 stays 1.0)."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    snapshot = [{"orderId": 11, "side": "BUY", "price": "100"}]
    info = {11: {"status": "NEW"}}
    bot, cfg, _ = _bot(monkeypatch, state, open_orders=snapshot, final_executed={11: 1.0}, order_info=info)
    _failing_cancel(bot, {11})
    await bot.kill()

    snapshot.clear()
    info[11] = {"status": "FILLED"}
    bot._verify_fill = AsyncMock(return_value="filled")
    await bot._sync_open_orders(cfg, state)

    await bot.kill()
    assert bot._lot_quantity(state, 100.0) == pytest.approx(1.0)


@pytest.mark.asyncio
async def test_retry_with_unknown_status_waits_for_the_drain(monkeypatch):
    """Neither an unknown status nor a live partial read is booked; the
    final 0.4 is, once the exchange reports the order final."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0)
    snapshot = [{"orderId": 99, "side": "BUY", "price": "100"}]
    bot, _, _ = _bot(monkeypatch, state, open_orders=snapshot, final_executed={99: 0.4})
    bot._unresolved_cancels[("BTCTHB", "99")] = ("BUY", 100.0, None)
    get = bot._http.get.side_effect
    calls = {"n": 0}

    async def flaky_then_partial(url, params=None):
        if url.endswith("/api/trade/order-status"):
            calls["n"] += 1
            if calls["n"] == 1:
                return _response(502)
            if calls["n"] == 2:
                return _response(200, {"status": "PARTIALLY_FILLED", "executedQty": "0.1"})
        return await get(url, params=params)

    bot._http.get.side_effect = flaky_then_partial
    first = await bot.kill()
    assert first["contained"] is False  # the drain's read was a stale live one
    assert 100.0 not in state.lot_qty

    retry = await bot.kill()
    assert retry["contained"] is True
    assert state.lot_qty[100.0] == pytest.approx(0.4)


@pytest.mark.asyncio
async def test_filled_buy_adds_to_a_partial_lot_at_its_price(monkeypatch):
    """A 0.4 lot from a cancelled partial BUY plus a 1.0 BUY filled at the
    same price is a 1.4 lot."""
    state = RealGridState(
        symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "12"},
        filled_buy_prices={100.0: 0}, buy_fill_prices={100.0: time.time()}, lot_qty={100.0: 0.4},
    )
    bot, cfg, _ = _bot(monkeypatch, state, final_executed={12: 1.0}, order_info={12: {"status": "FILLED"}})
    bot._verify_fill = AsyncMock(return_value="filled")

    await bot._sync_open_orders(cfg, state)

    assert state.lot_qty[100.0] == pytest.approx(1.4)


@pytest.mark.asyncio
async def test_unknown_verdict_does_not_retire_a_cancel_awaiting_retry(monkeypatch):
    """A kill cancelled the BUY but could not read it: an unverifiable sync
    must not assume a fill and drop the retry record."""
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0, active_buys={100.0: "11"})
    bot, cfg, _ = _bot(monkeypatch, state)
    bot._unresolved_cancels[("BTCTHB", "11")] = ("BUY", 100.0, None)
    bot._verify_fill = AsyncMock(return_value="unknown")

    await bot._sync_open_orders(cfg, state)

    assert state.active_buys == {100.0: "11"}
    assert ("BTCTHB", "11") in bot._unresolved_cancels
    assert 100.0 not in state.filled_buy_prices


@pytest.mark.asyncio
async def test_unresolved_cancels_survive_a_restart(monkeypatch):
    store = {}

    class FakeRedis:
        async def set(self, key, value):
            store[key] = value

        async def get(self, key):
            return store.get(key)

    state = _lots(p100=1.0)
    bot, _, _ = _bot(monkeypatch, state)
    bot._redis = FakeRedis()
    bot._save_state = RealGridBot._save_state.__get__(bot)
    bot._unresolved_cancels[("BTCTHB", "7")] = ("SELL", 96.0, 100.0)
    await bot._save_state("BTCTHB")

    restarted = RealGridBot(configs=[RealGridConfig(symbol="BTCTHB")])
    restarted._redis = FakeRedis()
    restarted.states["BTCTHB"] = RealGridState(symbol="BTCTHB")
    assert await restarted._load_state("BTCTHB")

    assert restarted._unresolved_cancels == {("BTCTHB", "7"): ("SELL", 96.0, 100.0)}


class _FakeRedis:
    def __init__(self, store):
        self.store = store

    async def set(self, key, value):
        self.store[key] = value

    async def get(self, key):
        return self.store.get(key)


@pytest.mark.asyncio
async def test_sync_retry_drops_the_active_sell_and_survives_a_restart(monkeypatch):
    """The snapshot still lists the SELL while order-status says CANCELED:
    the retry books 0.3 once, drops the active reference, and a restart
    cannot book it again."""
    state = _lots(p100=1.0)
    state.active_sells = {110.0: "9"}
    snapshot = [{"orderId": 9, "status": "NEW", "side": "SELL", "price": "110"}]
    bot, cfg, _ = _bot(monkeypatch, state, open_orders=snapshot, final_executed={9: 0.3})
    bot._unresolved_cancels[("BTCTHB", "9")] = ("SELL", 110.0, None)
    store = {}
    bot._redis = _FakeRedis(store)
    bot._save_state = RealGridBot._save_state.__get__(bot)

    await bot._sync_open_orders(cfg, state)
    assert 110.0 not in state.active_sells
    assert state.lot_qty[100.0] == pytest.approx(0.7)
    await bot._save_state("BTCTHB")

    restarted, cfg2, _ = _bot(monkeypatch, RealGridState(symbol="BTCTHB", current_order_size=1.0), open_orders=snapshot, final_executed={9: 0.3})
    restarted._redis = _FakeRedis(store)
    assert await restarted._load_state("BTCTHB")
    state2 = restarted.states["BTCTHB"]
    restarted._verify_fill = AsyncMock(return_value="cancelled")
    await restarted._sync_open_orders(cfg2, state2)

    assert state2.lot_qty[100.0] == pytest.approx(0.7)
    assert state2.booked_sells["9"][0] == pytest.approx(0.3)


@pytest.mark.asyncio
async def test_live_status_after_a_cancel_is_not_a_final_quantity(monkeypatch):
    """A stale NEW/PARTIALLY_FILLED read after a confirmed cancel is
    unknown: the lot is not shrunk and the order is kept for retry."""
    state = _lots(p100=1.0)
    state.stop_loss_triggered = {100.0: True}
    state.stop_loss_orders = {100.0: 96.0}
    bot, _, _ = _bot(monkeypatch, state, final_executed={7: 0.1}, order_info={7: {"status": "PARTIALLY_FILLED"}})

    await bot._after_confirmed_sell_cancel("BTCTHB", state, 96.0, "7")

    assert state.lot_qty.get(100.0, 1.0) == pytest.approx(1.0)
    assert state.stop_loss_triggered[100.0] is True
    assert ("BTCTHB", "7") in bot._unresolved_cancels


@pytest.mark.asyncio
async def test_go_run_started_and_stopped_during_the_drain_is_not_contained(monkeypatch):
    state = RealGridState(symbol="BTCTHB", current_order_size=1.0)
    bot, _, _ = _bot(monkeypatch, state)
    get = bot._http.get.side_effect
    reads = {"n": 0}

    async def run_seq_moves(url, params=None):
        if url.endswith("/api/bot/status"):
            reads["n"] += 1
            return _response(200, {"is_active": False, "run_seq": 4 if reads["n"] == 1 else 5})
        return await get(url, params=params)

    bot._http.get.side_effect = run_seq_moves
    result = await bot.kill()

    assert result["contained"] is False
    assert any(f.get("reason") == "go_running" for f in result["failed"])
