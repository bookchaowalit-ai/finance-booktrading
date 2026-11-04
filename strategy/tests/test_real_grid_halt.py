"""Regression tests for persistent mainnet safety halts."""

import time
from unittest.mock import AsyncMock, MagicMock

import pytest

from app.real_grid_bot import RealGridBot, RealGridConfig, RealGridState
from app.risk_manager import RiskManager


@pytest.mark.asyncio
async def test_halted_symbol_skips_daily_rebalance():
    """A daily rollover must not re-enable or rebalance a halted symbol."""
    config = RealGridConfig(symbol="BTCTHB")
    bot = RealGridBot(configs=[config])
    state = RealGridState(
        symbol="BTCTHB",
        last_daily_reset=time.time() - 86_401,
        halted=True,
    )
    bot.states[config.symbol] = state
    bot._run_capital_allocation = MagicMock()
    bot._send_comprehensive_digest = AsyncMock()
    bot._rebalance_grid = AsyncMock()

    await bot._tick(config)

    assert state.halted is True
    bot._rebalance_grid.assert_not_awaited()


@pytest.mark.asyncio
async def test_halted_symbol_removes_only_exchange_confirmed_cancellations():
    """A halted bot may clear canceled orders but never assume an unknown fill."""
    config = RealGridConfig(symbol="BTCTHB")
    bot = RealGridBot(configs=[config])
    state = RealGridState(
        symbol="BTCTHB",
        halted=True,
        active_buys={2220408.0: "canceled-order"},
        active_sells={2225000.0: "unknown-order"},
    )
    bot.states[config.symbol] = state
    bot._http = object()
    bot._verify_fill = AsyncMock(side_effect=["cancelled", "unknown"])
    # Nothing executed: the cancel can be booked. An unknown executed qty
    # would keep the BUY tracked so its fill is not lost.
    bot._final_executed_qty = AsyncMock(return_value=0.0)
    bot._journal = MagicMock(record_exit=AsyncMock())
    bot._save_state = AsyncMock()

    await bot._reconcile_halted_orders(config, state)

    assert state.active_buys == {}
    assert state.active_sells == {2225000.0: "unknown-order"}
    bot._save_state.assert_awaited_once_with("BTCTHB")


def test_risk_manager_daily_reset_preserves_halt():
    """Risk accounting resets must not clear a manual-review kill switch."""
    manager = RiskManager()
    manager.state.halted = True
    manager.state.halt_reason = "extreme volatility"
    manager.state.last_daily_reset = time.time() - 86_401

    manager._check_daily_reset()

    assert manager.state.halted is True
    assert manager.state.halt_reason == "extreme volatility"
