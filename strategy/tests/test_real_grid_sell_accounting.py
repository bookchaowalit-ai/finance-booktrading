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
