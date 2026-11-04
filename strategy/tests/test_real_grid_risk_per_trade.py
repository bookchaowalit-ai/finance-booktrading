"""Real grid BUY orders must be checked against risk_per_trade_pct."""

from unittest.mock import AsyncMock, MagicMock

import pytest

import app.real_grid_bot as real_grid_bot
from app.real_grid_bot import RealGridBot, RealGridConfig, RealGridState
from app.risk_manager import RiskManager

PRICE = 2_000_000.0
SIZE = 0.00005  # 100 THB notional


def _bot(monkeypatch, balances):
    monkeypatch.setattr(real_grid_bot, "BINANCE_TH_MAINNET", True)
    cfg = RealGridConfig(symbol="BTCTHB", order_size=SIZE)
    bot = RealGridBot(configs=[cfg])
    state = RealGridState(symbol="BTCTHB", current_order_size=SIZE)
    bot.states[cfg.symbol] = state
    bot._risk = RiskManager()
    bot._can_place_buy = AsyncMock(return_value=True)
    bot._can_place_sell = AsyncMock(return_value=True)
    bot._fetch_balances = AsyncMock(return_value=balances)
    bot._http = MagicMock()
    bot._http.post = AsyncMock()
    return bot, cfg, state


@pytest.mark.asyncio
async def test_buy_passes_account_value_to_risk_check(monkeypatch):
    bot, cfg, state = _bot(monkeypatch, {"THB": 500.0, "BTC": 0.0001})
    spy = MagicMock(return_value=(False, "blocked in test"))
    bot._risk.check_order_allowed = spy

    await bot._place_grid_order(cfg, state, "BUY", PRICE)

    # 500 THB free + 0.0001 BTC * 2,000,000 = 700 THB
    assert spy.call_args.kwargs["current_balance_thb"] == pytest.approx(700.0)


@pytest.mark.asyncio
async def test_buy_over_risk_per_trade_is_blocked(monkeypatch):
    bot, cfg, state = _bot(monkeypatch, {"THB": 500.0})

    placed = await bot._place_grid_order(cfg, state, "BUY", PRICE)

    # 100 THB is 20% of 500 THB, over the 1% default
    assert placed is False
    bot._http.post.assert_not_awaited()


@pytest.mark.asyncio
async def test_buy_blocked_when_balance_unknown(monkeypatch):
    bot, cfg, state = _bot(monkeypatch, {})

    placed = await bot._place_grid_order(cfg, state, "BUY", PRICE)

    assert placed is False
    bot._http.post.assert_not_awaited()


@pytest.mark.asyncio
async def test_sell_is_not_limited_by_risk_per_trade(monkeypatch):
    """A SELL reduces exposure; the per-trade cap must not trap a position."""
    bot, cfg, state = _bot(monkeypatch, {"THB": 500.0, "BTC": 0.001})
    spy = MagicMock(return_value=(False, "blocked in test"))
    bot._risk.check_order_allowed = spy

    await bot._place_grid_order(cfg, state, "SELL", PRICE)

    assert spy.call_args.kwargs.get("current_balance_thb", 0.0) == 0.0


@pytest.mark.asyncio
async def test_stop_loss_of_an_accumulated_lot_is_not_size_capped(monkeypatch):
    """Three fills at one price make a 300 THB lot; its stop must still go out.

    The 200 THB order-size cap bounds new exposure. A SELL only exits held
    coins (balance-checked), so capping it would leave the lot unprotected.
    """
    bot, cfg, state = _bot(monkeypatch, {"THB": 500.0, "BTC": 0.00015})
    resp = MagicMock(status_code=201)
    resp.json.return_value = {"order": {"orderId": "9"}}
    bot._http.post.return_value = resp
    bot._journal = MagicMock(record_entry=AsyncMock())
    bot._webhook = MagicMock(send_fill_alert=AsyncMock())
    state.filled_buy_prices[PRICE] = 1
    state.lot_qty[PRICE] = 3 * SIZE

    await bot._check_stop_loss(cfg, state, 1_920_000.0)

    bot._http.post.assert_awaited_once()
    assert bot._http.post.call_args.kwargs["json"]["quantity"] == pytest.approx(3 * SIZE)
    assert state.stop_loss_triggered.get(PRICE) is True


def test_order_size_cap_applies_to_buys_only():
    risk = RiskManager()
    big = risk.config.max_order_size_thb * 2 / PRICE
    assert risk.check_order_allowed("BTCTHB", "BUY", big, PRICE)[0] is False
    assert risk.check_order_allowed("BTCTHB", "SELL", big, PRICE)[0] is True
