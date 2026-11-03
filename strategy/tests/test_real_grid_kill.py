"""Kill switch containment: stop the Go loop, cancel exchange orders, explicit reset."""

import asyncio
from unittest.mock import AsyncMock, MagicMock

import httpx
import pytest

import app.real_grid_bot as real_grid_bot
from app.real_grid_bot import RealGridBot, RealGridConfig, RealGridState
from app.risk_manager import RiskManager


def _response(status, payload=None):
    return httpx.Response(status, json=payload or {}, request=httpx.Request("GET", "http://backend"))


def _bot(monkeypatch, stop_status=200, open_orders=None):
    """Bot with a fake backend; returns (bot, posted urls)."""
    open_orders = open_orders if open_orders is not None else {
        "BTCTHB": [{"orderId": 11}, {"orderId": 12}],
        "ETHTHB": [{"orderId": 21}],
    }
    configs = [RealGridConfig(symbol=s) for s in ("BTCTHB", "ETHTHB")]
    bot = RealGridBot(configs=configs)
    bot.states = {
        "BTCTHB": RealGridState(symbol="BTCTHB", active_buys={2_000_000.0: "11"}, active_sells={2_100_000.0: "12"}),
        "ETHTHB": RealGridState(symbol="ETHTHB", active_buys={80_000.0: "21"}),
    }
    bot._journal = MagicMock(record_exit=AsyncMock())
    bot._save_state = AsyncMock()
    risk = RiskManager()
    bot._risk = risk
    monkeypatch.setattr(real_grid_bot, "get_risk_manager", lambda: risk)
    monkeypatch.setattr(real_grid_bot, "get_real_grid_bot", lambda: bot, raising=False)

    posted = []

    async def post(url, json=None):
        posted.append((url, json))
        if url.endswith("/api/bot/stop"):
            return _response(stop_status)
        return _response(200)

    async def get(url, params=None):
        return _response(200, {"orders": open_orders.get(params["symbol"], [])})

    bot._http = MagicMock(post=AsyncMock(side_effect=post), get=AsyncMock(side_effect=get))
    return bot, posted


@pytest.mark.asyncio
async def test_kill_stops_go_loop_first_then_cancels_every_open_order(monkeypatch):
    bot, posted = _bot(monkeypatch)

    result = await bot.kill()

    urls = [u for u, _ in posted]
    assert urls[0].endswith("/api/bot/stop"), "the Go loop must stop before orders are drained"
    cancelled = sorted(body["orderId"] for u, body in posted if u.endswith("/api/trade/cancel-order"))
    assert cancelled == [11, 12, 21]
    assert result["go_bot_stopped"] is True
    assert result["cancelled"] == 3
    assert result["failed"] == []
    assert bot._enabled is False
    assert bot.states["BTCTHB"].active_buys == {} and bot.states["BTCTHB"].active_sells == {}
    assert bot.states["ETHTHB"].active_buys == {}


@pytest.mark.asyncio
async def test_kill_treats_go_not_running_as_stopped(monkeypatch):
    bot, _ = _bot(monkeypatch, stop_status=409)
    result = await bot.kill()
    assert result["go_bot_stopped"] is True


@pytest.mark.asyncio
async def test_kill_still_cancels_when_go_stop_fails(monkeypatch):
    bot, _ = _bot(monkeypatch, stop_status=500)
    result = await bot.kill()
    assert result["go_bot_stopped"] is False
    assert result["cancelled"] == 3
    assert result["contained"] is False


@pytest.mark.asyncio
async def test_kill_reports_orders_it_could_not_cancel(monkeypatch):
    bot, _ = _bot(monkeypatch)

    async def post(url, json=None):
        if url.endswith("/api/trade/cancel-order") and json["orderId"] == 12:
            return _response(502)
        return _response(200)

    bot._http.post = AsyncMock(side_effect=post)
    result = await bot.kill()
    assert result["failed"] == [{"symbol": "BTCTHB", "orderId": 12}]
    assert result["contained"] is False
    # A failed cancel stays tracked so reconciliation can still see it.
    assert bot.states["BTCTHB"].active_sells == {2_100_000.0: "12"}


@pytest.mark.asyncio
async def test_risk_halt_triggers_containment(monkeypatch):
    bot, _ = _bot(monkeypatch)
    bot.contain = AsyncMock(return_value={})

    bot._risk._halt("Daily loss limit hit")
    await asyncio.sleep(0)

    assert bot._enabled is False
    bot.contain.assert_awaited_once()


def test_enable_refuses_while_risk_manager_halted(monkeypatch):
    bot, _ = _bot(monkeypatch)
    bot._enabled = False
    bot._risk.state.halted = True
    bot._risk.state.halt_reason = "drawdown"

    assert bot.enable() is False
    assert bot._enabled is False
    assert bot._risk.state.halted is True, "enable() must not clear the risk halt"


def test_reset_kill_switch_does_not_restart_trading(monkeypatch):
    bot, _ = _bot(monkeypatch)
    bot._enabled = False
    bot._risk.state.halted = True

    bot._risk.reset_kill_switch()

    assert bot._risk.state.halted is False
    assert bot._enabled is False, "reset must not re-enable the bot; enable is its own step"
    assert bot.enable() is True
    assert bot._enabled is True


@pytest.mark.asyncio
async def test_enable_endpoint_is_409_while_risk_halted(monkeypatch):
    from fastapi import FastAPI

    import infrastructure.api.app as api_module

    bot, _ = _bot(monkeypatch)
    bot._risk.state.halted = True
    monkeypatch.setattr(api_module, "API_TOKEN", "t")
    app = FastAPI()
    app.state.config = {}
    api_module.register_routes(app)

    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.post("/api/real-grid/enable", headers={"Authorization": "Bearer t"})
    assert resp.status_code == 409
    assert bot._risk.state.halted is True


@pytest.mark.asyncio
async def test_kill_waits_for_in_flight_order_and_blocks_its_submission(monkeypatch):
    """A kill during a balance check must not let that order reach the exchange."""
    monkeypatch.setattr(real_grid_bot, "BINANCE_TH_MAINNET", True)
    bot, posted = _bot(monkeypatch, open_orders={})
    cfg = bot.configs[0]
    state = bot.states[cfg.symbol]
    state.current_order_size = 0.00005
    gate = asyncio.Event()

    async def slow_buy_check(*_args):
        await gate.wait()
        return True

    bot._can_place_buy = slow_buy_check
    bot._account_value_quote = AsyncMock(return_value=1_000_000.0)
    bot._risk.check_order_allowed = MagicMock(return_value=(True, "OK"))

    place = asyncio.create_task(bot._place_grid_order(cfg, state, "BUY", 2_000_000.0))
    await asyncio.sleep(0)
    kill = asyncio.create_task(bot.kill())
    await asyncio.sleep(0.05)
    assert not kill.done(), "containment must wait for the in-flight order"

    gate.set()
    assert await place is False
    result = await kill
    assert not any(u.endswith("/api/trade/order") for u, _ in posted)
    assert result["contained"] is True


@pytest.mark.asyncio
async def test_kill_endpoint_fails_loudly_when_not_contained(monkeypatch):
    from fastapi import FastAPI

    import infrastructure.api.app as api_module

    bot, _ = _bot(monkeypatch, stop_status=500)
    monkeypatch.setattr(api_module, "API_TOKEN", "t")
    app = FastAPI()
    app.state.config = {}
    api_module.register_routes(app)

    transport = httpx.ASGITransport(app=app)
    async with httpx.AsyncClient(transport=transport, base_url="http://test") as client:
        resp = await client.post("/api/real-grid/kill", headers={"Authorization": "Bearer t"})
    assert resp.status_code == 503
    assert resp.json()["contained"] is False
    assert bot._enabled is False
