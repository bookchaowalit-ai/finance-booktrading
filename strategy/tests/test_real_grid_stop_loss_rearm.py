"""A stop-loss sell that leaves the book unfilled must re-arm protection."""

from unittest.mock import AsyncMock

import pytest

from app.real_grid_bot import RealGridBot, RealGridConfig, RealGridState


@pytest.mark.asyncio
async def test_stop_loss_rearms_after_its_sell_is_cancelled():
    cfg = RealGridConfig(symbol="ETHTHB", buy_only=False, stop_loss_pct=3.0)
    bot = RealGridBot(configs=[cfg])
    buy = 100_000.0
    state = RealGridState(
        symbol="ETHTHB",
        filled_buy_prices={buy: True},
        buy_fill_prices={buy: 1.0},
        stop_loss_triggered={buy: True},
        stop_loss_orders={buy: 96_000.0},
        active_sells={},  # the stop sell was cancelled (e.g. by kill containment)
    )
    bot.states[cfg.symbol] = state
    bot._place_grid_order = AsyncMock(return_value=True)

    # First tick after the cancel, and a second one: protection must come back.
    await bot._check_stop_loss(cfg, state, 95_000.0)
    await bot._check_stop_loss(cfg, state, 95_000.0)

    bot._place_grid_order.assert_awaited()
    assert bot._place_grid_order.await_args.args[2] == "SELL"
    assert state.stop_loss_triggered[buy] is True
