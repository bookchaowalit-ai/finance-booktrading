"""Stop-loss protection after its sell leaves the book."""

import time
from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest

from app.real_grid_bot import RealGridBot, RealGridConfig, RealGridState

BUY = 100_000.0
STOP = 96_000.0


def _setup():
    cfg = RealGridConfig(symbol="ETHTHB", buy_only=False, stop_loss_pct=3.0)
    bot = RealGridBot(configs=[cfg])
    state = RealGridState(
        symbol="ETHTHB",
        filled_buy_prices={BUY: 0},
        buy_fill_prices={BUY: time.time()},
        stop_loss_triggered={BUY: True},
        stop_loss_orders={BUY: STOP},
        active_sells={STOP: "77"},
    )
    bot.states[cfg.symbol] = state
    bot._journal = MagicMock(record_exit=AsyncMock())
    bot._save_state = AsyncMock()
    bot._place_grid_order = AsyncMock(return_value=True)
    return bot, cfg, state


@pytest.mark.asyncio
async def test_confirmed_cancel_by_containment_rearms_stop_loss():
    bot, cfg, state = _setup()
    req = httpx.Request("GET", "http://backend")

    async def get(url, params=None):
        if url.endswith("/api/bot/status"):
            return httpx.Response(200, json={"is_active": False}, request=req)
        if url.endswith("/api/trade/order-status"):
            return httpx.Response(200, json={"status": "CANCELED", "executedQty": "0"}, request=req)
        return httpx.Response(200, json={"orders": [{"orderId": 77, "side": "SELL", "executedQty": "0"}]}, request=req)

    async def post(url, json=None):
        return httpx.Response(200, json={}, request=req)

    bot._http = MagicMock(get=AsyncMock(side_effect=get), post=AsyncMock(side_effect=post))
    await bot.kill()
    assert state.active_sells == {}

    await bot._check_stop_loss(cfg, state, 95_000.0)

    bot._place_grid_order.assert_awaited_once()
    assert bot._place_grid_order.await_args.args[2] == "SELL"


@pytest.mark.asyncio
async def test_unconfirmed_disappearance_does_not_place_a_second_stop():
    """An order that vanished with an unknown outcome may have filled."""
    bot, cfg, state = _setup()
    state.active_sells = {}

    await bot._check_stop_loss(cfg, state, 95_000.0)
    await bot._check_stop_loss(cfg, state, 95_000.0)

    bot._place_grid_order.assert_not_awaited()


@pytest.mark.asyncio
async def test_exchange_confirmed_cancel_while_halted_rearms_stop_loss():
    bot, cfg, state = _setup()
    state.halted = True
    bot._http = object()
    bot._verify_fill = AsyncMock(return_value="cancelled")
    bot._final_executed_qty = AsyncMock(return_value=0.0)

    await bot._reconcile_halted_orders(cfg, state)

    assert state.stop_loss_triggered[BUY] is False
    assert BUY not in state.stop_loss_orders


@pytest.mark.asyncio
async def test_stale_cancel_of_stop_sell_rearms_stop_loss():
    bot, cfg, state = _setup()
    state.order_times["77"] = time.time() - bot.MAX_ORDER_AGE_SECONDS - 1
    bot._cancel_order = AsyncMock(return_value=True)
    bot._final_executed_qty = AsyncMock(return_value=0.0)

    await bot._cancel_stale_orders(cfg, state, STOP)

    assert state.active_sells == {}
    assert state.stop_loss_triggered[BUY] is False


@pytest.mark.asyncio
async def test_failed_stale_cancel_keeps_the_order_tracked():
    bot, cfg, state = _setup()
    state.order_times["77"] = time.time() - bot.MAX_ORDER_AGE_SECONDS - 1
    bot._cancel_order = AsyncMock(return_value=False)

    await bot._cancel_stale_orders(cfg, state, STOP)

    assert state.active_sells == {STOP: "77"}, "an unconfirmed cancel must stay visible to sync"
    assert state.stop_loss_orders == {BUY: STOP}


@pytest.mark.asyncio
async def test_partly_filled_stop_sell_rearms_for_the_remaining_quantity():
    bot, cfg, state = _setup()
    state.lot_qty = {BUY: 1.0}
    req = httpx.Request("GET", "http://backend")

    async def get(url, params=None):
        if url.endswith("/api/bot/status"):
            return httpx.Response(200, json={"is_active": False}, request=req)
        if url.endswith("/api/trade/order-status"):
            return httpx.Response(200, json={"status": "CANCELED", "executedQty": "0.4"}, request=req)
        return httpx.Response(
            200, json={"orders": [{"orderId": 77, "side": "SELL", "executedQty": "0.4"}]}, request=req
        )

    async def post(url, json=None):
        return httpx.Response(200, json={}, request=req)

    bot._http = MagicMock(get=AsyncMock(side_effect=get), post=AsyncMock(side_effect=post))
    result = await bot.kill()

    assert result["partial_fills"][0]["orderId"] == 77
    assert state.lot_qty[BUY] == pytest.approx(0.6)
    await bot._check_stop_loss(cfg, state, 95_000.0)
    bot._place_grid_order.assert_awaited_once()
    assert bot._place_grid_order.await_args.kwargs["quantity"] == pytest.approx(0.6)



@pytest.mark.asyncio
async def test_stale_cancel_reads_executed_before_rearming():
    bot, cfg, state = _setup()
    state.lot_qty = {BUY: 1.0}
    state.order_times["77"] = time.time() - bot.MAX_ORDER_AGE_SECONDS - 1
    bot._cancel_order = AsyncMock(return_value=True)
    bot._final_executed_qty = AsyncMock(return_value=0.4)
    bot._http = object()

    await bot._cancel_stale_orders(cfg, state, STOP)

    assert state.lot_qty[BUY] == pytest.approx(0.6)
    assert state.stop_loss_triggered[BUY] is False


@pytest.mark.asyncio
async def test_stale_cancel_does_not_rearm_when_executed_unknown():
    bot, cfg, state = _setup()
    state.order_times["77"] = time.time() - bot.MAX_ORDER_AGE_SECONDS - 1
    bot._cancel_order = AsyncMock(return_value=True)
    bot._final_executed_qty = AsyncMock(return_value=None)
    bot._http = object()

    await bot._cancel_stale_orders(cfg, state, STOP)
    await bot._check_stop_loss(cfg, state, 95_000.0)

    bot._place_grid_order.assert_not_awaited()


def test_filled_sell_pnl_uses_the_lot_quantity():
    bot, cfg, state = _setup()
    state.lot_qty = {BUY: 0.4}
    qty = bot._sell_fill_qty(cfg, state, BUY)
    assert qty == pytest.approx(0.4)


def test_partial_sell_fill_shrinks_lot_instead_of_closing():
    bot, cfg, state = _setup()
    state.lot_qty = {BUY: 1.0}
    state.stop_loss_orders = {}
    state.stop_loss_triggered = {}
    qty = bot._close_lot_for_sell(cfg, state, 102_000.0, executed_qty=0.4)
    assert qty == (BUY, pytest.approx(0.4))
    assert state.lot_qty[BUY] == pytest.approx(0.6)
    assert BUY in state.filled_buy_prices
