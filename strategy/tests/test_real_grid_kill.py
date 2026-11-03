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


def _bot(monkeypatch, stop_status=200, open_orders=None, final_executed=None, go_symbol="", go_symbols=None, status_ok=True):
    """Bot with a fake backend; returns (bot, posted urls).

    ``final_executed`` maps orderId -> executedQty that order-status reports
    after the cancel (default 0 for every listed order; None = lookup fails).
    """
    open_orders = open_orders if open_orders is not None else {
        "BTCTHB": [{"orderId": 11}, {"orderId": 12}],
        "ETHTHB": [{"orderId": 21}],
    }
    if final_executed is None:
        final_executed = {o["orderId"]: 0 for orders in open_orders.values() for o in orders}
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
        if url.endswith("/api/bot/status"):
            if not status_ok:
                return _response(502)
            return _response(200, {"is_active": bool(go_symbol), "symbol": go_symbol, "symbols": go_symbols or []})
        if url.endswith("/api/trade/order-status"):
            oid = int(params["orderId"])
            executed = final_executed.get(oid)
            if executed is None:
                return _response(502)
            return _response(200, {"status": "CANCELED", "executedQty": str(executed)})
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


@pytest.mark.asyncio
async def test_enable_refused_while_containment_runs(monkeypatch):
    bot, _ = _bot(monkeypatch)
    gate = asyncio.Event()
    original_get = bot._http.get

    async def slow_get(url, params=None):
        await gate.wait()
        return await original_get(url, params=params)

    bot._http.get = AsyncMock(side_effect=slow_get)
    kill = asyncio.create_task(bot.kill())
    await asyncio.sleep(0.01)

    assert bot.enable() is False
    assert bot._enabled is False
    gate.set()
    await kill
    assert bot.enable() is True


@pytest.mark.asyncio
async def test_partially_filled_buy_becomes_a_protected_lot(monkeypatch):
    bot, _ = _bot(
        monkeypatch,
        open_orders={"BTCTHB": [{"orderId": 11, "side": "BUY", "executedQty": "0.00002"}], "ETHTHB": []},
        final_executed={11: 0.00002},
    )
    state = bot.states["BTCTHB"]

    result = await bot.kill()

    assert 2_000_000.0 in state.filled_buy_prices, "the bought part must stay a position"
    assert 2_000_000.0 in state.buy_fill_prices
    assert result["partial_fills"] == [
        {"symbol": "BTCTHB", "orderId": 11, "side": "BUY", "executedQty": 0.00002}
    ]
    # Journal entry stays open: the trade happened, it was not a plain cancel.
    closed = [c.args[0] for c in bot._journal.record_exit.await_args_list]
    assert "11" not in closed


@pytest.mark.asyncio
async def test_overlapping_kills_share_one_containment(monkeypatch):
    bot, posted = _bot(monkeypatch)
    gate = asyncio.Event()
    original_get = bot._http.get

    async def slow_get(url, params=None):
        await gate.wait()
        return await original_get(url, params=params)

    bot._http.get = AsyncMock(side_effect=slow_get)
    first = asyncio.create_task(bot.kill())
    await asyncio.sleep(0.01)
    second = asyncio.create_task(bot.kill())
    await asyncio.sleep(0.01)
    assert bot.enable() is False
    gate.set()
    r1, r2 = await first, await second

    assert r1 == r2
    stops = [u for u, _ in posted if u.endswith("/api/bot/stop")]
    assert len(stops) == 1, "one containment for both kill requests"
    assert bot.enable() is True


@pytest.mark.asyncio
async def test_partial_buy_records_final_executed_quantity(monkeypatch):
    # Snapshot says 0.00002; it filled further before the cancel landed.
    bot, _ = _bot(
        monkeypatch,
        open_orders={"BTCTHB": [{"orderId": 11, "side": "BUY", "executedQty": "0.00002"}], "ETHTHB": []},
        final_executed={11: 0.00003},
    )
    result = await bot.kill()
    state = bot.states["BTCTHB"]
    assert state.lot_qty[2_000_000.0] == pytest.approx(0.00003)
    assert result["partial_fills"][0]["executedQty"] == pytest.approx(0.00003)


@pytest.mark.asyncio
async def test_unreconciled_cancel_is_not_contained(monkeypatch):
    bot, _ = _bot(monkeypatch, final_executed={11: 0, 12: None, 21: 0})
    result = await bot.kill()
    assert result["unreconciled"] == [{"symbol": "BTCTHB", "orderId": 12}]
    assert result["contained"] is False


@pytest.mark.asyncio
async def test_go_bot_symbol_is_drained_too(monkeypatch):
    bot, posted = _bot(
        monkeypatch,
        open_orders={"BTCTHB": [], "ETHTHB": [], "BTCUSDT": [{"orderId": 31}]},
        go_symbol="BTCUSDT",
    )
    result = await bot.kill()
    cancelled = [b["orderId"] for u, b in posted if u.endswith("/api/trade/cancel-order")]
    assert cancelled == [31]
    assert result["contained"] is True



@pytest.mark.asyncio
async def test_all_go_traded_symbols_are_drained(monkeypatch):
    bot, posted = _bot(
        monkeypatch,
        open_orders={"BTCTHB": [], "ETHTHB": [], "BTCUSDT": [{"orderId": 31}], "ETHUSDT": [{"orderId": 41}]},
        go_symbols=["BTCUSDT", "ETHUSDT"],
    )
    await bot.kill()
    cancelled = sorted(b["orderId"] for u, b in posted if u.endswith("/api/trade/cancel-order"))
    assert cancelled == [31, 41]


@pytest.mark.asyncio
async def test_go_status_failure_is_not_contained(monkeypatch):
    bot, _ = _bot(monkeypatch, status_ok=False)
    result = await bot.kill()
    assert result["contained"] is False


@pytest.mark.asyncio
async def test_kill_retry_still_drains_go_symbols_from_the_first_attempt(monkeypatch):
    bot, _ = _bot(
        monkeypatch,
        open_orders={"BTCTHB": [], "ETHTHB": [], "BTCUSDT": [{"orderId": 31}]},
        go_symbols=["BTCUSDT"],
    )
    real_post = bot._http.post.side_effect

    async def failing_cancel(url, json=None):
        if url.endswith("/api/trade/cancel-order"):
            return _response(502)
        return await real_post(url, json=json)

    bot._http.post = AsyncMock(side_effect=failing_cancel)
    assert (await bot.kill())["contained"] is False

    # Retry: Go no longer reports the symbol, the cancel now works.
    bot2_posted = []

    async def post(url, json=None):
        bot2_posted.append((url, json))
        return _response(200)

    bot._http.post = AsyncMock(side_effect=post)
    orig_get = bot._http.get.side_effect

    async def get(url, params=None):
        if url.endswith("/api/bot/status"):
            return _response(200, {"is_active": False, "symbols": []})
        return await orig_get(url, params=params)

    bot._http.get = AsyncMock(side_effect=get)
    result = await bot.kill()
    assert [b["orderId"] for u, b in bot2_posted if u.endswith("/api/trade/cancel-order")] == [31]
    assert result["contained"] is True


@pytest.mark.asyncio
async def test_unresolved_final_execution_keeps_order_tracked(monkeypatch):
    bot, _ = _bot(monkeypatch, final_executed={11: 0, 12: None, 21: 0})
    await bot.kill()
    assert bot.states["BTCTHB"].active_sells == {2_100_000.0: "12"}, "sync must still resolve it"


@pytest.mark.asyncio
async def test_partial_ordinary_sell_shrinks_its_lot(monkeypatch):
    bot, _ = _bot(
        monkeypatch,
        open_orders={"BTCTHB": [{"orderId": 12, "side": "SELL", "executedQty": "0.3"}], "ETHTHB": []},
        final_executed={12: 0.3},
    )
    state = bot.states["BTCTHB"]
    state.filled_buy_prices = {2_000_000.0: 0}
    state.buy_fill_prices = {2_000_000.0: 1.0}
    state.lot_qty = {2_000_000.0: 1.0}
    await bot.kill()
    assert state.lot_qty[2_000_000.0] == pytest.approx(0.7)


@pytest.mark.asyncio
async def test_go_symbols_are_read_after_the_stop(monkeypatch):
    """A signal trade during the stop adds a symbol; drain must see it."""
    bot, posted = _bot(monkeypatch, open_orders={"BTCTHB": [], "ETHTHB": [], "SOLUSDT": [{"orderId": 51}]})
    stopped = {"v": False}
    orig_post = bot._http.post.side_effect
    orig_get = bot._http.get.side_effect

    async def post(url, json=None):
        if url.endswith("/api/bot/stop"):
            stopped["v"] = True
        return await orig_post(url, json=json)

    async def get(url, params=None):
        if url.endswith("/api/bot/status"):
            return _response(200, {"symbols": ["SOLUSDT"] if stopped["v"] else []})
        return await orig_get(url, params=params)

    bot._http.post = AsyncMock(side_effect=post)
    bot._http.get = AsyncMock(side_effect=get)
    await bot.kill()
    assert [b["orderId"] for u, b in posted if u.endswith("/api/trade/cancel-order")] == [51]


@pytest.mark.asyncio
async def test_containment_waits_for_the_running_tick(monkeypatch):
    bot, posted = _bot(monkeypatch)
    async with bot._tick_lock:
        kill = asyncio.create_task(bot.kill())
        await asyncio.sleep(0.05)
        assert not any(u.endswith("/api/trade/cancel-order") for u, _ in posted), "drained during a tick"
    await kill
    assert any(u.endswith("/api/trade/cancel-order") for u, _ in posted)


@pytest.mark.asyncio
async def test_unresolved_cancel_is_rechecked_on_retry(monkeypatch):
    final = {11: 0, 12: None, 21: 0}
    bot, _ = _bot(monkeypatch, final_executed=final)
    assert (await bot.kill())["contained"] is False

    # Retry: order 12 is no longer open on the exchange and still unresolved.
    orig_get = bot._http.get.side_effect

    async def get(url, params=None):
        if url.endswith("/api/trade/open-orders"):
            return _response(200, {"orders": []})
        return await orig_get(url, params=params)

    bot._http.get = AsyncMock(side_effect=get)
    assert (await bot.kill())["contained"] is False, "12 still unresolved"

    final[12] = 0  # exchange now answers
    result = await bot.kill()
    assert result["contained"] is True
    assert bot.states["BTCTHB"].active_sells == {}
