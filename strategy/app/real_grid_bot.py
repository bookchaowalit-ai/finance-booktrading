"""
Real Grid Trading Bot v2
=========================
Places real buy/sell orders on Binance TH via the Go backend's /api/trade endpoints.

Key improvements over v1:
- Queries actual open orders from Binance TH (not just memory)
- Cancels stale orders that are too far from current price
- Tracks PnL from actual filled trades (not double-counting)
- Prevents duplicate orders at same price level

Usage:
    # Via FastAPI background task (auto-started with strategy service)
    # Or standalone: python -m app.real_grid_bot
"""

import asyncio
import json
import logging
import os
import time
from collections import deque
from dataclasses import dataclass, field
from typing import Dict, List, Optional, Set

import httpx

from app.backend_auth import backend_event_hooks

from app.risk_manager import get_risk_manager
from app.trade_journal import get_trade_journal, JournalEntry
from app.webhook_notifier import get_webhook_notifier

logger = logging.getLogger("real_grid_bot")

# ── Configuration ─────────────────────────────────────────────────────────────

BINANCE_PUBLIC_REST = os.getenv("BINANCE_PRICE_API", "https://api.binance.th")
BACKEND_API_BASE = os.getenv("BACKEND_API_BASE", "http://backend:8080")

# Mainnet safety: explicit confirmation required for real money trading
# BINANCE_TH_USE_TESTNET=true (or unset) keeps real trading disabled (safety mode)
# Real-money orders require an explicit BINANCE_TH_USE_TESTNET=false; unset means safety mode.
BINANCE_TH_MAINNET = os.getenv("BINANCE_TH_USE_TESTNET", "true").strip().lower() == "false"


@dataclass
class RealGridConfig:
    symbol: str
    grid_spacing_pct: float = 1.5       # % between grid levels (fixed mode)
    grid_levels: int = 3                # number of levels above/below (conservative)
    order_size: float = 0.00005         # base quantity per grid order (~104 THB)
    max_position: float = 0.001         # max position size in base asset
    poll_interval_sec: int = 60         # how often to check prices
    max_daily_loss_usd: float = 50.0    # stop trading if daily loss exceeds this
    max_open_orders: int = 10           # max simultaneous open orders
    stale_threshold_pct: float = 5.0    # cancel orders >5% away from current price
    # Volatility-adaptive spacing
    volatility_mode: str = "fixed"      # "fixed" or "atr"
    atr_period: int = 14                # ATR period for volatility calculation
    atr_multiplier: float = 1.5         # multiplier for ATR-based spacing
    min_spacing_pct: float = 0.5        # minimum spacing % (floor)
    max_spacing_pct: float = 5.0        # maximum spacing % (ceiling)
    # Auto-compounding: scale order size with accumulated profits
    auto_compound_enabled: bool = True  # enable auto-compounding
    compound_threshold_thb: float = 500.0  # profit threshold to trigger scaling
    compound_factor: float = 0.1        # scale factor per threshold (10% increase)
    max_compound_multiplier: float = 3.0  # cap at 3x base order size
    # Dip catcher mode: buy-only accumulation (no sells)
    buy_only: bool = False              # True = only place BUY orders, never SELL
    # Exchange rules
    tick_size: float = 1.0              # Price must be multiple of this (e.g., 1.0, 0.01, 0.001)
    # Stop-loss protection
    stop_loss_pct: float = 3.0          # sell if price drops this % below filled buy price


@dataclass
class RealGridState:
    """Tracks active grid orders for a symbol."""
    symbol: str
    # Tracked by price -> orderId (from Binance TH)
    active_buys: Dict[int, str] = field(default_factory=dict)
    active_sells: Dict[int, str] = field(default_factory=dict)
    last_price: float = 0.0
    trades_executed: int = 0
    daily_pnl: float = 0.0
    daily_trades: int = 0
    last_daily_reset: float = 0.0
    halted: bool = False   # kill switch
    # Track which trade IDs we've already counted for PnL
    counted_trade_ids: Set[str] = field(default_factory=set)
    # Auto-compounding: track cumulative profit for order size scaling
    cumulative_pnl: float = 0.0
    current_order_size: float = 0.0  # dynamically scaled order size
    # ── Performance metrics ──
    orders_placed: int = 0            # total orders placed (lifetime)
    orders_filled: int = 0            # total orders filled (lifetime)
    last_atr_spacing_pct: float = 0.0 # most recent ATR-computed spacing %
    atr_spacing_history: List[float] = field(default_factory=list)  # last N ATR spacing values
    profit_velocity_thb_per_day: float = 0.0  # rolling avg profit per day
    fill_rate: float = 0.0            # orders_filled / orders_placed
    last_fill_timestamp: float = 0.0  # epoch of last fill
    performance_start_time: float = 0.0  # when metrics tracking began
    # ── Actual PnL tracking (buy-sell matching) ──
    buy_fill_prices: Dict[int, float] = field(default_factory=dict)    # buy_price -> fill timestamp (unmatched open lots; FIFO fallback ordering)
    filled_buy_prices: Dict[int, float] = field(default_factory=dict)  # buy_price -> 0 until matched (keys = levels holding an open position)
    # ── Sharpe/Sortino tracking ──
    trade_returns: List[float] = field(default_factory=list)  # per-trade return % for risk-adjusted metrics
    daily_pnl_history: List[float] = field(default_factory=list)  # daily PnL snapshots for auto-tune
    # ── Capital allocation & regime detection ──
    regime: str = "normal"              # "low_vol", "normal", "high_vol", "extreme"
    allocation_weight: float = 1.0      # 0.0-2.0 multiplier on grid_levels from capital allocation
    atr_percentile: float = 50.0        # current ATR percentile (0-100)
    allocation_score: float = 0.0       # composite score for capital allocation ranking
    regime_history: List[str] = field(default_factory=list)  # last N regime classifications
    # ── Time-based stale order tracking ──
    order_times: Dict[str, float] = field(default_factory=dict)  # order_id -> placement epoch
    # ── Trend detection (bear market protection) ──
    trend: str = "neutral"  # "bullish", "neutral", "bearish"
    trend_paused: bool = False  # True when bearish trend detected, stops new buy orders
    # ── Stop-loss tracking ──
    stop_loss_triggered: Dict[int, bool] = field(default_factory=dict)  # buy_price -> True if stop-loss sell placed
    stop_loss_orders: Dict[float, float] = field(default_factory=dict)  # buy_price -> limit price of the placed stop sell (for chase/re-place)
    lot_qty: Dict[float, float] = field(default_factory=dict)  # buy_price -> base qty when it differs from order size (partial fills)
    # order_id -> (executed qty, net PnL, fee) already booked for a SELL; not persisted
    booked_sells: Dict[str, tuple] = field(default_factory=dict)


# ── Per-symbol default configs ────────────────────────────────────────────────
# Tuned for DIP CATCHER mode: buy-only accumulation at wider spacing
# BTC excluded — user handles BTC via Binance auto-DCA
# Bot catches dips on altcoins that DCA doesn't cover
SYMBOL_DEFAULTS = {
    "BTCTHB": {
        # BTC handled by Binance DCA — keep config but buy_only if ever activated
        "grid_spacing_pct": 3.0,
        # Micro-live gate: one level and one outstanding order maximum.
        "grid_levels": 1,
        "order_size": 0.00005,
        "max_position": 0.001,
        "max_daily_loss_usd": 50.0,
        "max_open_orders": 1,
        "volatility_mode": "fixed",
        "buy_only": True,
        "stale_threshold_pct": 15.0,
    },
    # ── Binance TH exchange rules ──────────────────────────────────────────
    # ETHTHB:   stepSize=0.0001, minNotional=100 THB, tickSize=1
    # BNBTHB:   stepSize=0.01,   minNotional=100 THB, tickSize=0.01
    # SOLTHB:   stepSize=0.01,   minNotional=100 THB, tickSize=0.01
    # XRPTHB:   stepSize=0.01,   minNotional=100 THB, tickSize=0.01
    # ASTERTHB: stepSize=0.01,   minNotional=100 THB, tickSize=0.01
    # ATHTHB:   stepSize=0.001,  minNotional=100 THB, tickSize=0.001
    # PLUMETHB: stepSize=0.01,   minNotional=100 THB, tickSize=0.001
    # VELOTHB:  stepSize=1.0,    minNotional=100 THB, tickSize=0.0001
    # ZENTTHB:  stepSize=1.0,    minNotional=100 THB, tickSize=0.0001
    # Every order MUST be ≥100 THB and match stepSize multiples.
    # ────────────────────────────────────────────────────────────────────────
    "ETHTHB": {
        "grid_spacing_pct": 2.0,        # fallback spacing when ATR data unavailable
        "grid_levels": 2,               # matches backtested atr-dynamic config
        "order_size": 0.002,            # ~110 THB per order (step=0.0001 ✓) — must be ≥100 THB min notional
        "max_position": 0.004,          # ~210 THB max exposure
        "max_daily_loss_usd": 50.0,
        # 30d backtest (2026-07-04): atr-dynamic 62% win / +61.8 THB / 1.0% maxDD
        # vs fixed 2% at 50% win / +60.6 THB / 7.4% maxDD — same profit, 7x less drawdown
        "volatility_mode": "atr",
        "buy_only": False,              # Full grid — buy dips + sell peaks
        "stale_threshold_pct": 2.5,     # Cancel if >2.5% from market (auto-reprice quickly)
        "tick_size": 1.0,               # Price must be multiple of 1
    },
    "BNBTHB": {
        "grid_spacing_pct": 2.0,        # 2% tighter spacing
        "grid_levels": 1,
        "order_size": 0.01,             # ~184 THB per order (step=0.01 ✓)
        "max_position": 0.02,           # ~368 THB max exposure
        "max_daily_loss_usd": 40.0,
        "volatility_mode": "fixed",
        "buy_only": False,              # Full grid — buy dips + sell peaks
        "stale_threshold_pct": 2.5,     # Cancel if >2.5% from market (auto-reprice quickly)
        "tick_size": 0.01,              # Price must be multiple of 0.01
    },
    "SOLTHB": {
        "grid_spacing_pct": 3.0,        # fallback spacing when ATR data unavailable
        "grid_levels": 2,               # matches backtested atr-dynamic config
        "order_size": 0.05,             # ~120 THB per order (step=0.01 ✓)
        "max_position": 0.10,           # ~240 THB max exposure
        "max_daily_loss_usd": 30.0,
        # 30d backtest (2026-07-04): atr-dynamic +130.8 THB / 3.0% maxDD
        # vs fixed 3% at +92.8 THB / 3.2% maxDD — 40% more profit, same drawdown
        "volatility_mode": "atr",
        "buy_only": False,              # Full grid — buy dips + sell peaks
        "stale_threshold_pct": 3.5,     # Cancel if >3.5% from market (auto-reprice quickly)
        "tick_size": 0.01,              # Price must be multiple of 0.01
    },
    "XRPTHB": {
        "grid_spacing_pct": 2.0,        # 2% tighter spacing
        "grid_levels": 1,
        "order_size": 3.0,              # ~102 THB per order (step=0.01 ✓)
        "max_position": 6.0,            # ~204 THB max exposure
        "max_daily_loss_usd": 25.0,
        "volatility_mode": "fixed",
        "buy_only": False,              # Full grid — buy dips + sell peaks
        "stale_threshold_pct": 2.5,     # Cancel if >2.5% from market (auto-reprice quickly)
        "tick_size": 0.01,              # Price must be multiple of 0.01
    },
    # ── Micro-cap altcoins (Binance TH only) ─────────────────────────────────
    "ASTERTHB": {
        "grid_spacing_pct": 4.0,        # 4% — high volatility micro-cap
        "grid_levels": 1,
        "order_size": 5.0,              # ~103 THB per order (step=0.01 ✓)
        "max_position": 10.0,
        "max_daily_loss_usd": 20.0,
        "volatility_mode": "fixed",
        "buy_only": False,
        "stale_threshold_pct": 4.5,     # Cancel if >4.5% from market (auto-reprice quickly)
        "tick_size": 0.01,              # Price must be multiple of 0.01
    },
    "ATHTHB": {
        "grid_spacing_pct": 3.0,        # 3% — mid volatility
        "grid_levels": 1,
        "order_size": 725.0,            # ~100 THB per order (step=0.001 ✓)
        "max_position": 1500.0,
        "max_daily_loss_usd": 15.0,
        "volatility_mode": "fixed",
        "buy_only": False,
        "stale_threshold_pct": 3.5,     # Cancel if >3.5% from market (auto-reprice quickly)
        "tick_size": 0.001,             # Price must be multiple of 0.001
    },
    "PLUMETHB": {
        "grid_spacing_pct": 4.0,        # 4% — RWA narrative, volatile
        "grid_levels": 1,
        "order_size": 325.0,            # ~100 THB per order (step=0.01 ✓)
        "max_position": 650.0,
        "max_daily_loss_usd": 15.0,
        "volatility_mode": "fixed",
        "buy_only": False,
        "stale_threshold_pct": 4.5,     # Cancel if >4.5% from market (auto-reprice quickly)
        "tick_size": 0.001,             # Price must be multiple of 0.001
    },
    "VELOTHB": {
        "grid_spacing_pct": 4.0,        # 4% — micro-cap, volatile
        "grid_levels": 1,
        "order_size": 936.0,            # ~100 THB per order (step=1.0 ✓)
        "max_position": 2000.0,
        "max_daily_loss_usd": 10.0,
        "volatility_mode": "fixed",
        "buy_only": False,
        "stale_threshold_pct": 4.5,     # Cancel if >4.5% from market (auto-reprice quickly)
        "tick_size": 0.0001,            # Price must be multiple of 0.0001
    },
    "ZENTTHB": {
        "grid_spacing_pct": 4.0,        # 4% — micro-cap, volatile
        "grid_levels": 1,
        "order_size": 1287.0,           # ~100 THB per order (step=1.0 ✓)
        "max_position": 2600.0,
        "max_daily_loss_usd": 10.0,
        "volatility_mode": "fixed",
        "buy_only": False,
        "stale_threshold_pct": 4.5,     # Cancel if >4.5% from market (auto-reprice quickly)
        "tick_size": 0.0001,            # Price must be multiple of 0.0001
    },
}

# ── Strategy Constants ─────────────────────────────────────────────────────────

# Binance TH fee: 0.1% per trade (maker+taker), round trip = 0.2%
# Minimum spacing must exceed fees to be profitable
BINANCE_TH_FEE_PCT = 0.1              # 0.1% per side
ROUND_TRIP_FEE_PCT = BINANCE_TH_FEE_PCT * 2  # 0.2% round trip
MIN_PROFITABLE_SPACING_PCT = 0.5      # spacing must be > 0.5% to profit after fees

# Account capital base (THB) used as the equity denominator for drawdown
# tracking. Cumulative PnL alone is NOT a valid equity curve: early on the
# peak is a few THB, so tiny dips register as huge percentage drawdowns.
GRID_CAPITAL_BASE_THB = float(os.getenv("GRID_CAPITAL_BASE_THB", "3000"))
# Portfolio-wide exposure is deliberately bounded below the account capital
# base.  This applies to new BUY exposure; SELLs remain available for exits.
GRID_MAX_PORTFOLIO_NOTIONAL_THB = float(
    os.getenv("GRID_MAX_PORTFOLIO_NOTIONAL_THB", str(GRID_CAPITAL_BASE_THB * 0.8))
)

# Regime detection thresholds (ATR percentile)
REGIME_THRESHOLDS = {
    "low_vol": 25,     # ATR < 25th percentile
    "normal_low": 50,  # 25th-50th
    "normal_high": 75, # 50th-75th
    "high_vol": 90,    # 75th-90th
    "extreme": 90,     # > 90th percentile
}

# Capital allocation: minimum tracking days before allocation kicks in
MIN_ALLOCATION_TRACKING_DAYS = 3


class RealGridBot:
    """
    Real grid trading bot that:
    1. Fetches live price from Binance TH public API
    2. Queries actual open orders from Binance TH via backend
    3. Cancels stale orders that moved too far from price
    4. Places new grid orders at proper levels
    5. Calculates PnL from actual filled trades
    6. Enforces safety controls (max position, daily loss, kill switch)
    """
    # 4 hours — auto-cancel unfilled orders. Was 30 min, which caused constant
    # churn in low-vol markets: grid orders sit ~1% from price, price rarely
    # moves 1% in 30 min, so orders expired before ever filling (132 placed,
    # ~1 fill on 2026-07-04). Price-drift cancellation still removes orders
    # that move too far away, so this doesn't loosen risk control.
    MAX_ORDER_AGE_SECONDS = 4 * 60 * 60

    def __init__(self, configs: Optional[List[RealGridConfig]] = None):
        # Read REAL_SYMBOLS from env (e.g., "BTCTHB" or "BTCTHB,ETHTHB")
        real_symbols_str = os.getenv("REAL_SYMBOLS", "BTCTHB")
        real_symbols = [s.strip() for s in real_symbols_str.split(",") if s.strip()]
        
        self.configs = configs or [
            RealGridConfig(
                symbol=sym,
                **SYMBOL_DEFAULTS.get(sym, SYMBOL_DEFAULTS.get("BTCTHB", {})),
            )
            for sym in real_symbols
        ]
        self.states: Dict[str, RealGridState] = {}
        self._running = False
        self._http: Optional[httpx.AsyncClient] = None
        self._enabled = True  # master kill switch
        # Order placements between their first await and the POST; contain()
        # waits for these so nothing lands on the exchange after the drain.
        self._inflight_orders = 0
        # Held by each tick round and by containment, so a drain never runs
        # while sync / stale cancels are mutating the same state.
        self._tick_lock = asyncio.Lock()
        # (symbol, orderId) cancelled by a kill whose final execution is not
        # known yet; every containment re-checks them.
        self._unresolved_cancels: Dict[tuple, str] = {}  # -> order side
        # Order POSTs whose outcome is unknown (timeout, 5xx): the order may
        # still go live, so containment waits them out (UNCERTAIN_GRACE_SEC).
        self._uncertain_submissions: List[Dict] = []
        # Orders cancelled while _sync_open_orders runs (superseded stops,
        # possibly several calls deep); its snapshot still lists them.
        self._sync_cancelled: Set[str] = set()
        # Notification queue for fill events (max 50)
        self._notifications: deque = deque(maxlen=50)
        # Redis client for state persistence (injected via set_redis)
        self._redis = None
        self._risk = get_risk_manager()
        # Webhook notifier for Telegram/Discord alerts
        self._webhook = get_webhook_notifier()
        # Health tracking: last tick time per symbol
        self._last_tick_time: Dict[str, float] = {}

    def set_redis(self, redis_client):
        """Inject Redis client for state persistence."""
        self._redis = redis_client
        logger.info("Real Grid Bot: Redis persistence enabled")

    async def _save_state(self, symbol: str):
        """Persist grid state to Redis (called after each tick)."""
        if not self._redis:
            return
        state = self.states.get(symbol)
        if not state:
            return
        try:
            data = {
                "active_buys": {str(k): v for k, v in state.active_buys.items()},
                "active_sells": {str(k): v for k, v in state.active_sells.items()},
                "last_price": state.last_price,
                "trades_executed": state.trades_executed,
                "daily_pnl": state.daily_pnl,
                "daily_trades": state.daily_trades,
                "last_daily_reset": state.last_daily_reset,
                "halted": state.halted,
                "counted_trade_ids": list(state.counted_trade_ids),
                "cumulative_pnl": state.cumulative_pnl,
                "current_order_size": state.current_order_size,
                # Performance metrics
                "orders_placed": state.orders_placed,
                "orders_filled": state.orders_filled,
                "last_atr_spacing_pct": state.last_atr_spacing_pct,
                "atr_spacing_history": state.atr_spacing_history[-50:],  # keep last 50
                "profit_velocity_thb_per_day": state.profit_velocity_thb_per_day,
                "fill_rate": state.fill_rate,
                "last_fill_timestamp": state.last_fill_timestamp,
                "performance_start_time": state.performance_start_time,
                "buy_fill_prices": {str(k): v for k, v in state.buy_fill_prices.items()},
                "filled_buy_prices": {str(k): v for k, v in state.filled_buy_prices.items()},
                "trade_returns": state.trade_returns[-500:],
                "daily_pnl_history": state.daily_pnl_history[-90:],
                "order_times": {str(k): v for k, v in state.order_times.items()},
                "stop_loss_triggered": {str(k): v for k, v in state.stop_loss_triggered.items()},
                "stop_loss_orders": {str(k): v for k, v in state.stop_loss_orders.items()},
                "lot_qty": {str(k): v for k, v in state.lot_qty.items()},
            }
            await self._redis.set(f"real_grid:{symbol}:state", json.dumps(data))
        except Exception as e:
            logger.debug("Failed to save state to Redis for %s: %s", symbol, e)

    async def _load_state(self, symbol: str) -> bool:
        """Restore grid state from Redis. Returns True if restored."""
        if not self._redis:
            return False
        try:
            raw = await self._redis.get(f"real_grid:{symbol}:state")
            if not raw:
                return False
            data = json.loads(raw)
            state = self.states[symbol]
            state.active_buys = {float(k): v for k, v in data.get("active_buys", {}).items()}
            state.active_sells = {float(k): v for k, v in data.get("active_sells", {}).items()}
            state.last_price = data.get("last_price", 0.0)
            state.trades_executed = data.get("trades_executed", 0)
            state.daily_pnl = data.get("daily_pnl", 0.0)
            state.daily_trades = data.get("daily_trades", 0)
            state.last_daily_reset = data.get("last_daily_reset", 0.0)
            state.halted = data.get("halted", False)
            state.counted_trade_ids = set(data.get("counted_trade_ids", []))
            state.cumulative_pnl = data.get("cumulative_pnl", 0.0)
            restored_size = data.get("current_order_size", 0.0)
            if restored_size > 0:
                state.current_order_size = restored_size
            # Restore performance metrics
            state.orders_placed = data.get("orders_placed", 0)
            state.orders_filled = data.get("orders_filled", 0)
            state.last_atr_spacing_pct = data.get("last_atr_spacing_pct", 0.0)
            state.atr_spacing_history = data.get("atr_spacing_history", [])
            state.profit_velocity_thb_per_day = data.get("profit_velocity_thb_per_day", 0.0)
            state.fill_rate = data.get("fill_rate", 0.0)
            state.last_fill_timestamp = data.get("last_fill_timestamp", 0.0)
            state.performance_start_time = data.get("performance_start_time", 0.0)
            # Restore buy-fill tracking
            state.buy_fill_prices = {float(k): v for k, v in data.get("buy_fill_prices", {}).items()}
            state.filled_buy_prices = {float(k): v for k, v in data.get("filled_buy_prices", {}).items()}
            state.trade_returns = data.get("trade_returns", [])
            state.daily_pnl_history = data.get("daily_pnl_history", [])
            # Restore order times
            state.order_times = {str(k): v for k, v in data.get("order_times", {}).items()}
            # Restore stop-loss tracking
            state.stop_loss_triggered = {float(k): v for k, v in data.get("stop_loss_triggered", {}).items()}
            state.stop_loss_orders = {float(k): v for k, v in data.get("stop_loss_orders", {}).items()}
            state.lot_qty = {float(k): v for k, v in data.get("lot_qty", {}).items()}
            logger.info(
                "Restored state for %s from Redis: buys=%d sells=%d pnl=%.2f cum_pnl=%.2f order_size=%.6f",
                symbol, len(state.active_buys), len(state.active_sells), state.daily_pnl,
                state.cumulative_pnl, state.current_order_size,
            )
            return True
        except Exception as e:
            logger.warning("Failed to load state from Redis for %s: %s", symbol, e)
            return False

    async def start(self):
        """Start the real grid bot."""
        self._running = True
        self._http = httpx.AsyncClient(timeout=15.0, event_hooks=backend_event_hooks())

        # Initialize risk manager and trade journal
        self._risk = get_risk_manager()
        self._journal = get_trade_journal()
        await self._journal.start(self._http)

        # ── Mainnet safety check ──
        if BINANCE_TH_MAINNET:
            logger.warning(
                "=" * 60 + "\n"
                "  REAL MONEY TRADING ENABLED (MAINNET)\n"
                "  Symbols: %s\n"
                "  If this is NOT intended, set BINANCE_TH_USE_TESTNET=true\n"
                + "=" * 60,
                ", ".join(c.symbol for c in self.configs),
            )
        else:
            logger.info(
                "Real Grid Bot running in TESTNET/SAFETY mode - real orders DISABLED"
            )

        logger.info("Real Grid Bot v2 started with %d symbol(s)", len(self.configs))

        for cfg in self.configs:
            self.states[cfg.symbol] = RealGridState(
                symbol=cfg.symbol,
                last_daily_reset=time.time(),
                current_order_size=cfg.order_size,  # Initialize with base order size
                performance_start_time=time.time(),  # Track when metrics began
            )
            # Attempt to restore previous state from Redis
            await self._load_state(cfg.symbol)

        # Check if backend is ready
        if not await self._check_backend():
            logger.warning("Backend not ready — will retry on first tick")

        while self._running:
            try:
                if not self._enabled:
                    await asyncio.sleep(30)
                    continue

                async with self._tick_lock:
                    for cfg in self.configs:
                        if not self._enabled:
                            break  # killed mid-round: free the lock for containment
                        await self._tick(cfg)
                await asyncio.sleep(self.configs[0].poll_interval_sec)
            except asyncio.CancelledError:
                break
            except Exception as e:
                logger.error("Real Grid Bot error: %s", e, exc_info=True)
                await asyncio.sleep(30)

        logger.info(
            "Real Grid Bot stopped. Trades: %d",
            sum(s.trades_executed for s in self.states.values()),
        )

    async def stop(self):
        """Stop the real grid bot."""
        self._running = False
        # Final state save to Redis
        for symbol in self.states:
            await self._save_state(symbol)
        if self._http:
            await self._http.aclose()

    def _containment_running(self) -> bool:
        task = getattr(self, "_contain_task", None)
        return getattr(self, "_containing", 0) > 0 or (task is not None and not task.done())

    def enable(self) -> bool:
        """Re-enable the bot after a kill. Returns False while the risk manager is halted.

        Enabling never clears a risk halt: reset it first, deliberately, via
        ``RiskManager.reset_kill_switch()`` (``POST /api/risk/reset``). It is
        also refused while a kill containment is still running.
        """
        if self._containment_running():
            logger.warning("Real Grid Bot enable refused: kill containment still running")
            return False
        risk = get_risk_manager()
        if risk.state.halted:
            logger.warning(
                "Real Grid Bot enable refused: risk manager halted (%s)", risk.state.halt_reason
            )
            return False
        self._enabled = True
        for state in self.states.values():
            state.halted = False
        logger.info("Real Grid Bot ENABLED")
        return True

    def _containment(self) -> "asyncio.Task":
        """The running containment task, or a new one. Overlapping kills share it."""
        task = getattr(self, "_contain_task", None)
        if task is None or task.done():
            task = asyncio.get_running_loop().create_task(self.contain())
            self._contain_task = task
        return task

    def disable(self):
        """Kill switch — stop all trading and start containment.

        From inside the event loop (e.g. a risk halt during a tick) this
        schedules containment; callers that can await should use ``kill()``.
        """
        self._enabled = False
        logger.warning("Real Grid Bot KILLED — all trading halted")
        try:
            self._containment()
        except RuntimeError:
            logger.warning("No running event loop: open orders NOT cancelled; call kill()")

    async def kill(self) -> Dict:
        """Disable trading and wait for the (shared) containment to finish."""
        self._enabled = False
        logger.warning("Real Grid Bot KILLED — all trading halted")
        return await asyncio.shield(self._containment())

    INFLIGHT_WAIT_SEC = 30.0
    # How long an order POST with an unknown outcome can take to show up in
    # open orders (the Go grid uses the same grace for its own submissions).
    UNCERTAIN_GRACE_SEC = 60.0

    def _lot_quantity(self, state: RealGridState, buy_price: float) -> float:
        """Base quantity held by a lot: recorded size, else the order size."""
        if buy_price in state.lot_qty:
            return state.lot_qty[buy_price]
        if state.current_order_size > 0:
            return state.current_order_size
        cfg = self._config_for(state.symbol)
        return cfg.order_size if cfg else 0.0

    def _config_for(self, symbol: str) -> Optional[RealGridConfig]:
        return next((cfg for cfg in self.configs if cfg.symbol == symbol), None)

    def _stop_lot_for(self, state: RealGridState, sell_price: float) -> Optional[float]:
        """The open lot whose stop-loss SELL sits at sell_price, if any."""
        for buy, placed in state.stop_loss_orders.items():
            if placed == sell_price and buy in state.buy_fill_prices:
                return buy
        return None

    def _lot_order(self, state: RealGridState, sell_price: float) -> List[float]:
        """Open lots in the order a SELL @ sell_price consumes them.

        1. The lot this sell is the stop-loss of.
        2. Else the grid pair: the highest buy strictly below the sell price
           (a grid sell sits one spacing above the buy it exits).
        3. Then the oldest lots by fill time.
        """
        first = self._stop_lot_for(state, sell_price)
        if first is None:
            below = [p for p in state.buy_fill_prices if p < sell_price]
            first = max(below) if below else None
        rest = sorted((p for p in state.buy_fill_prices if p != first), key=state.buy_fill_prices.get)
        return ([first] if first is not None else []) + rest

    def _close_lot(self, state: RealGridState, lot: float):
        for book in (state.filled_buy_prices, state.buy_fill_prices, state.lot_qty,
                     state.stop_loss_triggered, state.stop_loss_orders):
            book.pop(lot, None)

    def _consume_lots(self, state: RealGridState, sell_price: float, qty: float) -> tuple:
        """Take qty base off the lots a SELL @ sell_price sold.

        Returns (pieces, stops): pieces are (buy_price, qty) per lot sold, with
        buy_price 0.0 for quantity no lot covers; stops are prices of resting
        stop-loss SELLs sized for a lot this sell shrank or closed, which the
        caller must cancel (a confirmed cancel re-arms the remainder). A lot
        left partly open keeps its entry time; when this sell was its own
        stop-loss, the stop is re-armed for the rest.
        """
        stop_lot = self._stop_lot_for(state, sell_price)
        pieces, stops = [], []
        remaining = qty
        for lot in self._lot_order(state, sell_price):
            if remaining <= 1e-12:
                break
            base = self._lot_quantity(state, lot)
            take = min(base, remaining)
            if take <= 0:
                continue
            pieces.append((lot, take))
            remaining -= take
            resting = state.stop_loss_orders.get(lot)
            other_stop_live = lot != stop_lot and resting is not None and resting in state.active_sells
            if other_stop_live:
                stops.append(resting)
            if base - take <= 1e-12:
                self._close_lot(state, lot)
                continue
            state.lot_qty[lot] = base - take
            if lot == stop_lot:
                state.stop_loss_orders.pop(lot, None)
                state.stop_loss_triggered[lot] = False
        if remaining > 1e-12:
            pieces.append((0.0, remaining))
        return pieces, stops

    def _expected_sell_qty(self, state: RealGridState, sell_price: float) -> float:
        """What a SELL sold when the exchange did not say: its first lot's size."""
        lots = self._lot_order(state, sell_price)
        return self._lot_quantity(state, lots[0]) if lots else self._lot_quantity(state, 0.0)

    async def _realize_sell(
        self, state: RealGridState, sell_price: float, qty: float, order_id: str, exit_reason: str
    ) -> Optional[Dict]:
        """Book a SELL's execution: consume its lots, realize PnL and fees.

        qty is the order's total executed quantity; only what was not booked
        for this order before is booked now, so a retried or overlapping call
        cannot count it twice. PnL goes to the state, the risk manager (daily
        loss, drawdown) and the journal. Returns the booking, or None when
        nothing was new.
        """
        booked_qty, booked_net, booked_fee = state.booked_sells.get(order_id, (0.0, 0.0, 0.0))
        new_qty = qty - booked_qty
        if new_qty <= 1e-12:
            return None
        pieces, stops = self._consume_lots(state, sell_price, new_qty)

        cfg = self._config_for(state.symbol)
        fee_pct = BINANCE_TH_FEE_PCT / 100.0  # per side, each on its own notional
        gross = fee = 0.0
        for buy, q in pieces:
            if buy > 0:
                gross += (sell_price - buy) * q
                fee += buy * q * fee_pct
            else:
                # No lot covers it: estimate from grid spacing.
                spacing_pct = cfg.grid_spacing_pct if cfg else 0.0
                gross += sell_price * (spacing_pct / 100.0) * q
                fee += sell_price * q * fee_pct
            fee += sell_price * q * fee_pct
        net = gross - fee

        state.booked_sells[order_id] = (qty, booked_net + net, booked_fee + fee)
        while len(state.booked_sells) > 500:
            state.booked_sells.pop(next(iter(state.booked_sells)))

        state.daily_pnl += net
        state.cumulative_pnl += net
        notional = new_qty * sell_price
        state.trade_returns.append((net / notional) * 100 if notional > 0 else 0)
        if len(state.trade_returns) > 500:
            state.trade_returns = state.trade_returns[-500:]
        if state.performance_start_time > 0:
            elapsed_days = (time.time() - state.performance_start_time) / 86400
            if elapsed_days > 0.1:
                state.profit_velocity_thb_per_day = state.cumulative_pnl / elapsed_days
        if cfg and cfg.auto_compound_enabled:
            old_size = state.current_order_size or cfg.order_size
            new_size = self._calculate_compound_order_size(cfg, state)
            if new_size != old_size:
                state.current_order_size = new_size
                logger.info(
                    "[RealGrid %s] Auto-compound: order_size %.6f -> %.6f (cum_pnl=%.2f THB)",
                    state.symbol, old_size, new_size, state.cumulative_pnl,
                )

        self._risk.record_trade_result(state.symbol, net, is_win=(net > 0))
        # Drawdown is measured against account equity, not peak profit (a
        # 4 THB dip on a ~9 THB peak once read as a 42% drawdown, 2026-07-06).
        total_pnl = sum(s.cumulative_pnl for s in self.states.values())
        self._risk.update_drawdown(GRID_CAPITAL_BASE_THB + total_pnl)

        # The journal holds the order's whole realized round trip.
        try:
            await self._journal.record_exit(order_id, sell_price, exit_reason, booked_net + net, booked_fee + fee)
        except Exception as e:
            logger.warning("Journal exit (%s) failed: %s", exit_reason, e)

        return {
            "pieces": pieces,
            "qty": new_qty,
            "buy_price": pieces[0][0] if pieces else 0.0,
            "gross": gross,
            "fee": fee,
            "net": net,
            "stops": stops,
        }

    async def _book_cancelled_sell(self, state: RealGridState, sell_price: float, order_id: str, executed_qty: float):
        """Book a confirmed-cancelled SELL: realize what it executed, then
        re-arm its stop-loss for whatever the lot still holds.

        This closes the order's journal entry, once and with the outcome: the
        backend updates only OPEN entries, so cancel paths must not close it
        first (cancel with close_journal=False).
        """
        stop_lot = self._stop_lot_for(state, sell_price)
        if executed_qty > 0:
            booking = await self._realize_sell(state, sell_price, executed_qty, order_id, "PARTIAL_SELL_CANCELLED")
            # A stop resting for a lot this sell shrank is now oversized.
            # Containment cancels every order anyway; elsewhere cancel it here.
            if booking and booking["stops"] and not getattr(self, "_containing", 0):
                await self._cancel_superseded_stops(state.symbol, state, booking["stops"])
        else:
            try:
                await self._journal.record_exit(order_id, 0.0, "CANCELLED", 0.0)
            except Exception as e:
                logger.warning("Journal exit (cancel) failed: %s", e)
        if stop_lot is not None and state.stop_loss_orders.get(stop_lot) == sell_price:
            state.stop_loss_orders.pop(stop_lot, None)
            if stop_lot in state.buy_fill_prices:
                state.stop_loss_triggered[stop_lot] = False

    async def _cancel_superseded_stops(self, symbol: str, state: RealGridState, stop_prices: List[float]) -> Set[str]:
        """Cancel resting stop-loss SELLs sized for lots another sell shrank or closed.

        The confirmed cancel books any fill it had and re-arms the stop for
        what the lot still holds. A failed cancel leaves the stop live and
        tracked (still triggered), so no second stop is placed beside it.
        Returns the order IDs it cancelled.
        """
        cancelled: Set[str] = set()
        for price in stop_prices:
            oid = state.active_sells.get(price)
            if not oid:
                continue
            if not await self._cancel_order(symbol, oid, close_journal=False):
                logger.error(
                    "[RealGrid %s] Stop-loss @ %s (order %s) is sized for a lot another sell shrank; "
                    "cancel failed, it stays live",
                    symbol, price, oid,
                )
                continue
            state.active_sells.pop(price, None)
            state.order_times.pop(oid, None)
            cancelled.add(oid)
            # A sync in progress must not re-import it from its snapshot.
            self._sync_cancelled.add(oid)
            await self._after_confirmed_sell_cancel(symbol, state, price, oid)
        return cancelled

    async def _forget_cancelled_order(self, state: RealGridState, order_id: str, executed_qty: float = 0.0):
        """Drop a confirmed-cancelled order from local state.

        A BUY with executed quantity becomes an open lot of that size so
        stop-loss covers it; a SELL books what it executed and re-arms its
        stop-loss for whatever the lot still holds.
        """
        for price, oid in list(state.active_buys.items()):
            if oid == order_id:
                del state.active_buys[price]
                if executed_qty > 0:
                    state.filled_buy_prices[price] = 0
                    state.buy_fill_prices[price] = time.time()
                    state.lot_qty[price] = executed_qty
        for price, oid in list(state.active_sells.items()):
            if oid == order_id:
                del state.active_sells[price]
                await self._book_cancelled_sell(state, price, order_id, executed_qty)
        state.order_times.pop(order_id, None)

    async def _after_confirmed_sell_cancel(self, symbol: str, state: RealGridState, sell_price: float, order_id: str):
        """Read the final executed quantity of a cancelled SELL, then book it.

        If the exchange cannot tell us, nothing is re-armed (a stop sized for
        the full lot could oversell); the lot is logged for review.
        """
        executed = await self._final_executed_qty(self._http, symbol, order_id)
        if executed is None:
            logger.error(
                "[RealGrid %s] Cancelled SELL @ %s (order %s): executed qty unknown — "
                "stop-loss not re-armed, review this lot",
                symbol, sell_price, order_id,
            )
            return
        await self._book_cancelled_sell(state, sell_price, order_id, executed)

    def _sell_fill_qty(self, cfg: RealGridConfig, state: RealGridState, buy_price: float) -> float:
        """Quantity a filled SELL closed: the lot's recorded size, else the order size."""
        if buy_price in state.lot_qty:
            return state.lot_qty[buy_price]
        return state.current_order_size if state.current_order_size > 0 else cfg.order_size

    async def _wait_inflight_orders(self) -> bool:
        """Wait until no order placement is mid-flight. False on timeout."""
        deadline = time.monotonic() + self.INFLIGHT_WAIT_SEC
        while getattr(self, "_inflight_orders", 0) > 0:
            if time.monotonic() >= deadline:
                return False
            await asyncio.sleep(0.01)
        return True

    async def _final_executed_qty(self, client, symbol: str, order_id: str):
        """Executed quantity the exchange reports after a cancel, or None."""
        if client is None or not hasattr(client, "get"):
            return None
        try:
            resp = await client.get(
                f"{BACKEND_API_BASE}/api/trade/order-status",
                params={"symbol": symbol, "orderId": order_id},
            )
            if resp.status_code == 200:
                return float(resp.json().get("executedQty") or 0)
        except Exception as e:
            logger.warning("Kill: order-status for %s %s failed: %s", symbol, order_id, e)
        return None

    async def _kill_cancel_execution(self, client, result: Dict, symbol: str, order_id: str, side: str) -> Optional[float]:
        """Final executed quantity of an order containment cancelled, or None.

        Unknown execution keeps the order in local state and in
        _unresolved_cancels, so sync and the next containment re-check it.
        """
        # The open-orders snapshot can be stale: the order may have filled
        # further before the cancel landed.
        executed = await self._final_executed_qty(client, symbol, order_id)
        if executed is None:
            result["unreconciled"].append({"symbol": symbol, "orderId": int(order_id) if order_id.isdigit() else order_id})
            self._unresolved_cancels[(symbol, order_id)] = side
        return executed

    async def _book_kill_cancel(self, result: Dict, symbol: str, order_id: str, side: str, executed: float):
        """Book one order containment cancelled, from its final executed quantity."""
        if executed > 0:
            result["partial_fills"].append({
                "symbol": symbol,
                "orderId": int(order_id) if order_id.isdigit() else order_id,
                "side": side,
                "executedQty": executed,
            })
        state = self.states.get(symbol)
        tracked_sell = bool(state and order_id in state.active_sells.values())
        if executed <= 0 and not tracked_sell:
            # A tracked SELL's entry is closed by _book_cancelled_sell.
            try:
                await self._journal.record_exit(order_id, 0.0, "CANCELLED", 0.0)
            except Exception as e:
                logger.warning("Journal exit (kill cancel) failed: %s", e)
        if state:
            await self._forget_cancelled_order(state, order_id, executed)

    async def contain(self) -> Dict:
        """Stop the Go bot loop, then cancel every open exchange order.

        Order: stop the Go bot (its Stop joins its loops), read the symbols
        it traded, wait for in-flight Python placements, then, holding the
        tick lock, re-check earlier unresolved cancels and cancel every open
        order on each configured and Go-traded symbol, reading each one's
        final executed quantity. ``contained`` is
        True only when all of that is confirmed.
        """
        client = self._http or httpx.AsyncClient(timeout=15.0, event_hooks=backend_event_hooks())
        result: Dict = {
            "go_bot_stopped": False,
            "cancelled": 0,
            "failed": [],
            "partial_fills": [],
            "unreconciled": [],
            "uncertain": [],
            "inflight_settled": False,
        }
        self._containing = getattr(self, "_containing", 0) + 1
        try:
            try:
                resp = await client.post(f"{BACKEND_API_BASE}/api/bot/stop", json={})
                # 409 = no Go bot loop running, which is what we want.
                result["go_bot_stopped"] = resp.status_code in (200, 409)
                if not result["go_bot_stopped"]:
                    logger.error("Kill: Go bot stop failed: %s %s", resp.status_code, resp.text)
            except Exception as e:
                logger.error("Kill: Go bot stop failed: %s", e)

            # Stop and status run before the tick lock so a slow tick cannot
            # delay halting the Go bot; only the drain waits for the tick.
            # Read after the stop: Go keeps the run's traded symbols after Stop,
            # and this includes any symbol a signal traded while stopping.
            # Go symbols are remembered until a containment fully succeeds, so
            # a retry after the Go bot stopped still drains them.
            go_symbols = getattr(self, "_go_drain_symbols", None)
            if go_symbols is None:
                go_symbols = self._go_drain_symbols = set()
            try:
                resp = await client.get(f"{BACKEND_API_BASE}/api/bot/status")
                if resp.status_code != 200:
                    raise RuntimeError(f"HTTP {resp.status_code}")
                status = resp.json()
                # A Start between the stop and this read: the Go loop may be
                # placing orders again, so nothing drained now is final.
                if status.get("is_active"):
                    logger.error("Kill: Go bot is running again after the stop")
                    result["failed"].append({"symbol": None, "orderId": None, "reason": "go_running"})
                # The drain lists and cancels on the current exchange only.
                current = status.get("exchange") or ""
                for other in status.get("traded_exchanges") or []:
                    if other != current:
                        logger.error("Kill: Go traded on %s; this drain only reaches %s", other, current)
                        result["failed"].append({"symbol": None, "orderId": None, "reason": "other_exchange", "exchange": other})
                for sym in [status.get("symbol") or ""] + list(status.get("symbols") or []):
                    if sym:
                        go_symbols.add(sym.upper())
                # Go submissions with an unknown outcome may go live after
                # the drain; Go keeps them listed until the exchange answers.
                for order in status.get("unresolved_orders") or []:
                    logger.error("Kill: Go order %s outcome unknown", order)
                    result["failed"].append({"symbol": order.get("symbol"), "orderId": None, "reason": "go_unresolved"})
                    if order.get("symbol"):
                        go_symbols.add(order["symbol"].upper())
            except Exception as e:
                logger.error("Kill: Go bot status unavailable, its symbols may be missed: %s", e)
                result["failed"].append({"symbol": None, "orderId": None, "reason": "go_status"})
            # Placements already past their _enabled check finish (or bail at
            # the pre-submit recheck) before the drain lists open orders.
            result["inflight_settled"] = await self._wait_inflight_orders()
            if not result["inflight_settled"]:
                logger.error("Kill: order placement still in flight after %.0fs", self.INFLIGHT_WAIT_SEC)

            async with self._tick_lock:
                # Every cancel's final execution is collected first and booked
                # after, BUYs before SELLs: a SELL listed before the BUY whose
                # lot it sold would otherwise find no lot to shrink.
                executions = []
                # Cancels from an earlier kill whose execution was unknown:
                # they are off the book now, so only this re-check finds them.
                for (symbol, order_id), side in list(self._unresolved_cancels.items()):
                    del self._unresolved_cancels[(symbol, order_id)]
                    executed = await self._kill_cancel_execution(client, result, symbol, order_id, side)
                    if executed is not None:
                        executions.append((symbol, order_id, side, executed))

                symbols = [cfg.symbol for cfg in self.configs]
                symbols += sorted(sym for sym in go_symbols if sym not in symbols)

                for symbol in symbols:
                    listed_at = time.time()
                    try:
                        resp = await client.get(
                            f"{BACKEND_API_BASE}/api/trade/open-orders", params={"symbol": symbol}
                        )
                        resp.raise_for_status()
                        orders = resp.json().get("orders") or []
                    except Exception as e:
                        logger.error("Kill: open orders for %s unavailable: %s", symbol, e)
                        result["failed"].append({"symbol": symbol, "orderId": None})
                        continue
                    # An uncertain POST older than the grace period would be
                    # in this listing if it went live, and is cancelled below.
                    self._uncertain_submissions = [
                        u for u in self._uncertain_submissions
                        if u["symbol"] != symbol or u["at"] > listed_at - self.UNCERTAIN_GRACE_SEC
                    ]
                    for order in orders:
                        order_id = order.get("orderId")
                        if not await self._cancel_order(symbol, str(order_id), client=client, close_journal=False):
                            result["failed"].append({"symbol": symbol, "orderId": order_id})
                            continue
                        result["cancelled"] += 1
                        side = order.get("side", "")
                        executed = await self._kill_cancel_execution(client, result, symbol, str(order_id), side)
                        if executed is not None:
                            executions.append((symbol, str(order_id), side, executed))

                def is_buy(item):
                    symbol, order_id, side, _ = item
                    state = self.states.get(symbol)
                    return side == "BUY" or bool(state and order_id in state.active_buys.values())

                for item in sorted(executions, key=lambda item: not is_buy(item)):
                    await self._book_kill_cancel(result, *item)
                for symbol in symbols:
                    if symbol in self.states:
                        await self._save_state(symbol)
            result["uncertain"] = [dict(u) for u in self._uncertain_submissions]
        finally:
            self._containing -= 1
            if client is not self._http:
                await client.aclose()

        result["contained"] = (
            result["go_bot_stopped"]
            and result["inflight_settled"]
            and not result["failed"]
            and not result["unreconciled"]
            and not result["uncertain"]
        )
        if result["contained"]:
            self._go_drain_symbols = set()
        log = logger.warning if result["contained"] else logger.error
        log("Kill containment: %s", result)
        return result

    def update_config(self, symbol: str, **kwargs) -> bool:
        """Update grid config for a symbol.
        
        Args:
            symbol: Trading pair (e.g., "BTCTHB")
            **kwargs: Config fields to update (grid_spacing_pct, grid_levels, order_size, etc.)
        
        Returns:
            True if config was updated, False if symbol not found
        """
        for cfg in self.configs:
            if cfg.symbol == symbol:
                for key, value in kwargs.items():
                    if hasattr(cfg, key):
                        setattr(cfg, key, value)
                        logger.info("[RealGrid %s] Config updated: %s = %s", symbol, key, value)
                return True
        return False

    def get_config(self, symbol: str) -> Optional[Dict]:
        """Get current grid config for a symbol."""
        for cfg in self.configs:
            if cfg.symbol == symbol:
                return {
                    "symbol": cfg.symbol,
                    "grid_spacing_pct": cfg.grid_spacing_pct,
                    "grid_levels": cfg.grid_levels,
                    "order_size": cfg.order_size,
                    "max_position": cfg.max_position,
                    "poll_interval_sec": cfg.poll_interval_sec,
                    "max_daily_loss_usd": cfg.max_daily_loss_usd,
                    "max_open_orders": cfg.max_open_orders,
                    "stale_threshold_pct": cfg.stale_threshold_pct,
                    # Volatility-adaptive fields
                    "volatility_mode": cfg.volatility_mode,
                    "atr_period": cfg.atr_period,
                    "atr_multiplier": cfg.atr_multiplier,
                    "min_spacing_pct": cfg.min_spacing_pct,
                    "max_spacing_pct": cfg.max_spacing_pct,
                }
        return None

    async def _check_backend(self) -> bool:
        """Check if Go backend is reachable and ready."""
        try:
            resp = await self._http.get(f"{BACKEND_API_BASE}/api/trade/status")
            if resp.status_code == 200:
                data = resp.json()
                return data.get("ready_to_trade", False)
        except Exception:
            pass
        return False

    async def _tick(self, cfg: RealGridConfig):
        """One tick: sync orders, fetch price, cancel stale, place new."""
        state = self.states[cfg.symbol]
        # Track last tick time for health monitoring
        self._last_tick_time[cfg.symbol] = time.time()

        # Daily reset
        if time.time() - state.last_daily_reset > 86400:
            # Capture daily PnL before resetting
            _daily_pnl = state.daily_pnl
            _daily_trades = state.daily_trades
            
            # Send daily summary before resetting (per-symbol if trades occurred)
            if _daily_trades > 0:
                await self._webhook.send_daily_summary(
                    symbol=cfg.symbol,
                    daily_pnl=_daily_pnl,
                    daily_trades=_daily_trades,
                )
            
            # Record daily PnL snapshot for auto-tune and Sharpe/Sortino
            state.daily_pnl_history.append(_daily_pnl)
            if len(state.daily_pnl_history) > 90:
                state.daily_pnl_history = state.daily_pnl_history[-90:]
            
            state.daily_pnl = 0.0
            state.daily_trades = 0
            state.last_daily_reset = time.time()
            # A safety halt is persistent. Daily accounting must not silently
            # re-enable mainnet trading; only the explicit enable/reset path
            # may clear state.halted.
            state.counted_trade_ids.clear()
            
            # Auto-tune compound threshold based on performance
            if _daily_trades > 0:
                self._auto_tune_compound_threshold(cfg, state)
            
            # Capital allocation: rebalance grid levels across symbols
            self._run_capital_allocation()
            
            # Send comprehensive daily digest (all symbols)
            await self._send_comprehensive_digest()
            
            # Auto-rebalance only when the symbol is not halted. A daily
            # accounting rollover must never bypass a persistent kill switch.
            if not state.halted:
                await self._rebalance_grid(cfg, state)
            else:
                logger.info(
                    "[RealGrid %s] Daily rollover kept safety halt active; "
                    "skipping rebalance",
                    cfg.symbol,
                )

        # Safety: daily loss limit
        if state.daily_pnl < -cfg.max_daily_loss_usd:
            if not state.halted:
                logger.warning(
                    "[RealGrid %s] Daily loss limit hit: $%.2f — HALTING",
                    cfg.symbol, state.daily_pnl,
                )
                state.halted = True
                # Send kill switch alert
                await self._webhook.send_kill_switch_alert(
                    symbol=cfg.symbol,
                    reason=f"Daily loss limit hit: ${state.daily_pnl:.2f}",
                )
            return

        if state.halted:
            # A safety halt must block trading, but it must not leave canceled
            # exchange orders permanently stuck in the persisted local state.
            # Reconcile only explicit cancellations; unknown/filled outcomes
            # remain pending manual review and are never assumed to be fills.
            await self._reconcile_halted_orders(cfg, state)
            return

        # Step 1: Sync open orders from Binance TH
        await self._sync_open_orders(cfg, state)

        # Step 2: Fetch current price
        price = await self._fetch_price(cfg.symbol)
        if price <= 0:
            return

        # ── Flash-crash circuit breaker (Fix 4) ──
        if state.last_price > 0:
            price_drop_pct = ((state.last_price - price) / state.last_price) * 100
            if price_drop_pct > 5.0:
                if not state.halted:
                    logger.warning(
                        "[RealGrid %s] FLASH CRASH: price dropped %.1f%% (%d -> %d) — HALTING",
                        cfg.symbol, price_drop_pct, int(state.last_price), int(price),
                    )
                    state.halted = True
                    await self._webhook.send_kill_switch_alert(
                        symbol=cfg.symbol,
                        reason=f"Flash crash detected: {price_drop_pct:.1f}% drop ({int(state.last_price)} -> {int(price)})",
                    )
                state.last_price = price
                return

        state.last_price = price

        # Step 3: Cancel stale orders (too far from current price)
        await self._cancel_stale_orders(cfg, state, price)

        # Step 4: Calculate PnL from filled trades
        await self._update_pnl(cfg, state)

        # Step 5: Detect market regime and adjust parameters
        regime = await self._detect_regime(cfg, state)
        self._apply_regime_adjustment(cfg, state, regime)
        
        # If halted by extreme regime, skip this tick
        if state.halted:
            await self._save_state(cfg.symbol)
            return

        # Step 5.5: Detect trend direction (bear market protection)
        bear_skip = await self._handle_bear_market(cfg, state, price)
        if bear_skip:
            logger.info(
                "[RealGrid %s] price=%.6g trend=%s — buy orders paused (bear market)",
                cfg.symbol, price, state.trend
            )
            await self._save_state(cfg.symbol)
            return

        # Step 5.6: Check stop-loss on filled buy positions
        await self._check_stop_loss(cfg, state, price)

        # Step 6: Calculate dynamic spacing based on volatility mode
        if cfg.volatility_mode == "atr":
            spacing_pct = await self._calculate_atr_spacing(cfg, price)
            # Track ATR spacing for performance analysis
            state.last_atr_spacing_pct = spacing_pct
            state.atr_spacing_history.append(spacing_pct)
            if len(state.atr_spacing_history) > 50:
                state.atr_spacing_history = state.atr_spacing_history[-50:]
        else:
            spacing_pct = cfg.grid_spacing_pct
        
        # Step 7: Apply fee-aware minimum spacing floor
        spacing_pct = self._apply_fee_floor(cfg, spacing_pct)
        
        spacing = price * (spacing_pct / 100.0)
        
        logger.info(
            "[RealGrid %s] price=%.6g spacing=%.6g (%.1f%%) mode=%s regime=%s trend=%s alloc=%.1f buys=%d sells=%d pnl=$%.2f",
            cfg.symbol, price, spacing, spacing_pct, cfg.volatility_mode,
            state.regime, state.trend, state.allocation_weight,
            len(state.active_buys), len(state.active_sells),
            state.daily_pnl,
        )

        # ── Portfolio-level exposure cap ──
        # The order-level guard below blocks new BUYs.  Do not return here:
        # exits must still be allowed when the portfolio is at its cap.
        total_notional = self._total_open_notional()
        if total_notional >= GRID_MAX_PORTFOLIO_NOTIONAL_THB:
            logger.debug(
                "[RealGrid %s] Portfolio exposure cap: %.0f THB >= %.0f — "
                "new BUYs blocked, exits remain available",
                cfg.symbol,
                total_notional,
                GRID_MAX_PORTFOLIO_NOTIONAL_THB,
            )

        for level in range(1, cfg.grid_levels + 1):
            # Round price to tick_size (Binance TH requires prices to be multiples of tickSize)
            tick = cfg.tick_size
            buy_price = round((price - (spacing * level)) / tick) * tick
            # Format price to avoid floating point issues
            buy_price = float(f"{buy_price:.10g}")

            total_orders = len(state.active_buys) + len(state.active_sells)
            if total_orders >= cfg.max_open_orders:
                break

            # Place LIMIT buy if not already active at this price
            if buy_price not in state.active_buys:
                await self._place_grid_order(cfg, state, "BUY", buy_price)

            # Place LIMIT sell only if NOT in buy-only (dip catcher) mode
            if not cfg.buy_only:
                # When capital-constrained (no buys possible), use tight offset
                # to get fills and free capital for the buy-sell cycle
                if len(state.active_buys) == 0 and len(state.active_sells) <= 1:
                    sell_offset_pct = 0.007  # 0.7% — profitable after 0.2% round-trip fees
                    sell_price = round((price * (1 + sell_offset_pct)) / tick) * tick
                else:
                    sell_price = round((price + (spacing * level)) / tick) * tick
                sell_price = float(f"{sell_price:.10g}")
                if sell_price not in state.active_sells:
                    await self._place_grid_order(cfg, state, "SELL", sell_price)

        # Persist state to Redis after each tick
        await self._save_state(cfg.symbol)

    def _total_open_orders(self) -> int:
        """Count active orders across every configured symbol."""
        return sum(
            len(state.active_buys) + len(state.active_sells)
            for state in self.states.values()
        )

    def _total_open_notional(self) -> float:
        """Estimate active-order notional across the whole real-grid portfolio."""
        config_by_symbol = {config.symbol: config for config in self.configs}
        total = 0.0
        for symbol, state in self.states.items():
            config = config_by_symbol.get(symbol)
            if not config:
                continue
            size = state.current_order_size if state.current_order_size > 0 else config.order_size
            total += sum(price * size for price in state.active_buys)
            total += sum(price * size for price in state.active_sells)
        return total

    async def _sync_open_orders(self, cfg: RealGridConfig, state: RealGridState):
        """Query actual open orders from Binance TH and update state.
        
        Only removes orders that no longer exist on exchange (filled/cancelled).
        Does NOT clear state first to prevent duplicate orders.
        """
        self._sync_cancelled = set()
        try:
            resp = await self._http.get(
                f"{BACKEND_API_BASE}/api/trade/open-orders",
                params={"symbol": cfg.symbol},
            )
            if resp.status_code != 200:
                return

            data = resp.json()
            orders = data.get("orders", [])

            # Build set of actual order IDs on exchange
            actual_order_ids = set()
            for order in orders:
                order_id = str(order.get("orderId", ""))
                status = order.get("status", "")
                if status in ("NEW", "PARTIALLY_FILLED"):
                    actual_order_ids.add(order_id)
                    # Track order time from exchange if not already known
                    if order_id not in state.order_times:
                        order_time_ms = order.get("time", 0)
                        if order_time_ms:
                            state.order_times[order_id] = order_time_ms / 1000.0
                        else:
                            # Unknown placement time (e.g. after Redis restart) —
                            # give it a fresh lifespan instead of backdating,
                            # so it doesn't get immediately cancelled on next tick.
                            state.order_times[order_id] = time.time()

            # ── Detect fills: orders that disappeared from exchange = FILLED ──
            # (This runs BEFORE _cancel_stale_orders, so missing orders were filled, not cancelled)

            # Detect filled BUYs
            filled_buy_prices = []
            for price, oid in list(state.active_buys.items()):
                if oid not in actual_order_ids:
                    filled_buy_prices.append((price, oid))

            for buy_price, oid in filled_buy_prices:
                # Verify against the exchange: a disappeared order is NOT
                # necessarily filled (manual cancel in the app, exchange purge)
                verdict = await self._verify_fill(cfg.symbol, oid)
                if verdict == "open":
                    continue  # open-orders snapshot raced; still on the book
                if verdict == "cancelled":
                    logger.info(
                        "[RealGrid %s] BUY @ %d was cancelled externally (not filled) — no position booked",
                        cfg.symbol, buy_price,
                    )
                    try:
                        await self._journal.record_exit(oid, 0.0, "CANCELLED", 0.0)
                    except Exception as e:
                        logger.warning("Journal exit (external cancel) failed: %s", e)
                    del state.active_buys[buy_price]
                    state.order_times.pop(oid, None)
                    continue
                if verdict == "unknown":
                    logger.warning(
                        "[RealGrid %s] Could not verify BUY @ %d (order %s) — assuming filled (legacy behavior)",
                        cfg.symbol, buy_price, oid,
                    )
                # Record buy fill price for later SELL PnL matching
                state.buy_fill_prices[buy_price] = time.time()  # fill time — used for FIFO fallback matching
                state.filled_buy_prices[buy_price] = 0  # SELL price unknown yet
                state.orders_filled += 1
                state.last_fill_timestamp = time.time()
                logger.info(
                    "[RealGrid %s] BUY filled @ %d (detected from exchange sync)",
                    cfg.symbol, buy_price,
                )
                self._notifications.append({
                    "type": "fill",
                    "side": "BUY",
                    "symbol": cfg.symbol,
                    "price": buy_price,
                    "quantity": state.current_order_size if state.current_order_size > 0 else cfg.order_size,
                    "profit": 0,
                    "trade_id": oid,
                    "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                    "message": f"{cfg.symbol} BUY filled @ {buy_price}",
                })
                await self._webhook.send_fill_alert(
                    symbol=cfg.symbol, side="BUY",
                    price=buy_price,
                    quantity=state.current_order_size if state.current_order_size > 0 else cfg.order_size,
                )
                # Close the journal entry for this order (position opened;
                # PnL is realized when the matching SELL fills)
                try:
                    await self._journal.record_exit(oid, buy_price, "FILLED_BUY", 0.0)
                except Exception as e:
                    logger.warning("Journal exit (BUY fill) failed: %s", e)
                del state.active_buys[buy_price]
                state.order_times.pop(oid, None)

            # Detect filled SELLs
            filled_sell_prices = []
            for price, oid in list(state.active_sells.items()):
                if oid not in actual_order_ids:
                    filled_sell_prices.append((price, oid))

            for sell_price, oid in filled_sell_prices:
                if state.active_sells.get(sell_price) != oid:
                    continue  # cancelled earlier in this pass (superseded stop)
                # Verify against the exchange before booking profit — an
                # externally-cancelled sell booked as a fill would fabricate PnL
                verdict = await self._verify_fill(cfg.symbol, oid)
                if verdict == "open":
                    continue  # open-orders snapshot raced; still on the book
                if verdict == "cancelled":
                    logger.info(
                        "[RealGrid %s] SELL @ %d was cancelled externally (not filled) — no PnL booked",
                        cfg.symbol, sell_price,
                    )
                    del state.active_sells[sell_price]
                    state.order_times.pop(oid, None)
                    await self._after_confirmed_sell_cancel(cfg.symbol, state, sell_price, oid)
                    continue
                if verdict == "unknown":
                    logger.warning(
                        "[RealGrid %s] Could not verify SELL @ %d (order %s) — assuming filled (legacy behavior)",
                        cfg.symbol, sell_price, oid,
                    )

                # Book the sell against the lots it sold, sized by what it
                # actually executed (a smaller fill shrinks its lot).
                executed = await self._final_executed_qty(self._http, cfg.symbol, oid)
                qty = executed if executed and executed > 0 else self._expected_sell_qty(state, sell_price)
                booking = await self._realize_sell(state, sell_price, qty, oid, "FILLED_SELL")
                if booking is None:
                    # Already booked (e.g. by a containment that cancelled it).
                    del state.active_sells[sell_price]
                    state.order_times.pop(oid, None)
                    continue
                actual_buy_price, qty = booking["buy_price"], booking["qty"]
                gross_profit, total_fee, net_profit = booking["gross"], booking["fee"], booking["net"]
                state.orders_filled += 1
                state.last_fill_timestamp = time.time()

                logger.info(
                    "[RealGrid %s] SELL filled @ %d qty=%.6f buy@%.0f gross=%.2f fee=%.2f net=%.2f THB",
                    cfg.symbol, sell_price, qty, actual_buy_price, gross_profit, total_fee, net_profit,
                )
                self._notifications.append({
                    "type": "fill",
                    "side": "SELL",
                    "symbol": cfg.symbol,
                    "price": sell_price,
                    "quantity": qty,
                    "profit": round(net_profit, 2),
                    "trade_id": oid,
                    "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                    "message": f"{cfg.symbol} SELL filled @ {sell_price} (net {net_profit:.1f} THB, fee {total_fee:.1f})",
                })
                await self._webhook.send_fill_alert(
                    symbol=cfg.symbol, side="SELL",
                    price=sell_price, quantity=qty, profit=net_profit,
                )
                del state.active_sells[sell_price]
                state.order_times.pop(oid, None)
                await self._cancel_superseded_stops(cfg.symbol, state, booking["stops"])

            # Update fill rate
            if state.orders_placed > 0:
                state.fill_rate = state.orders_filled / state.orders_placed

            # Clean up order_times for orders no longer on exchange
            stale_time_ids = [oid for oid in state.order_times if oid not in actual_order_ids]
            for oid in stale_time_ids:
                del state.order_times[oid]

            # Add new orders that exist on exchange but not in state
            for order in orders:
                order_id = str(order.get("orderId", ""))
                price = float(order.get("price", 0))
                side = order.get("side", "")
                status = order.get("status", "")

                if status not in ("NEW", "PARTIALLY_FILLED"):
                    continue
                if price <= 0 or order_id in self._sync_cancelled:
                    continue

                if side == "BUY" and price not in state.active_buys:
                    state.active_buys[price] = order_id
                elif side == "SELL" and price not in state.active_sells:
                    state.active_sells[price] = order_id

        except Exception as e:
            logger.warning("Failed to sync open orders: %s", e)

    async def _reconcile_halted_orders(self, cfg: RealGridConfig, state: RealGridState):
        """Remove only orders explicitly confirmed canceled while halted.

        This is intentionally narrower than ``_sync_open_orders``: a halted
        bot must not book fills or place replacements until an operator reviews
        the safety condition. It only clears local order references when the
        backend confirms CANCELED/EXPIRED/REJECTED.
        """
        if not self._http:
            return

        changed = False
        for side, order_map in (("BUY", state.active_buys), ("SELL", state.active_sells)):
            for price, order_id in list(order_map.items()):
                if order_map.get(price) != order_id:
                    continue  # removed earlier in this pass (superseded stop)
                verdict = await self._verify_fill(cfg.symbol, order_id)
                if verdict != "cancelled":
                    continue

                del order_map[price]
                state.order_times.pop(order_id, None)
                if side == "SELL":
                    await self._after_confirmed_sell_cancel(cfg.symbol, state, price, order_id)
                changed = True
                logger.info(
                    "[RealGrid %s] Removed canceled %s @ %.10g from halted state",
                    cfg.symbol,
                    side,
                    price,
                )
                journal = getattr(self, "_journal", None)
                if journal and side == "BUY":  # a SELL's entry is closed by its booking
                    try:
                        await journal.record_exit(order_id, 0.0, "CANCELLED", 0.0)
                    except Exception as exc:
                        logger.warning(
                            "Journal exit (halted %s cancellation) failed: %s",
                            side,
                            exc,
                        )

        if changed:
            await self._save_state(cfg.symbol)

    async def _cancel_stale_orders(self, cfg: RealGridConfig, state: RealGridState, current_price: float):
        """Cancel orders that are too far from current price OR too old.

        Only orders the backend confirmed cancelled leave local state; a
        failed cancel stays tracked so the next sync resolves it.
        """
        threshold_pct = cfg.stale_threshold_pct / 100.0
        lower_bound = int(current_price * (1 - threshold_pct))
        upper_bound = int(current_price * (1 + threshold_pct))
        now = time.time()

        def aged(order_id: str) -> bool:
            return now - state.order_times.get(order_id, now) > self.MAX_ORDER_AGE_SECONDS

        for side, book in (("BUY", state.active_buys), ("SELL", state.active_sells)):
            for price, order_id in list(book.items()):
                if book.get(price) != order_id:
                    continue  # removed earlier in this pass (superseded stop)
                drift = price < lower_bound if side == "BUY" else price > upper_bound
                if not drift and not aged(order_id):
                    continue
                # A SELL's journal entry is closed with its outcome below.
                if not await self._cancel_order(cfg.symbol, order_id, close_journal=(side == "BUY")):
                    logger.warning(
                        "[RealGrid %s] Cancel of stale %s @ %d not confirmed — keeping it tracked",
                        cfg.symbol, side, price,
                    )
                    continue
                state.order_times.pop(order_id, None)
                del book[price]
                if side == "SELL":
                    await self._after_confirmed_sell_cancel(cfg.symbol, state, price, order_id)
                logger.info(
                    "[RealGrid %s] Cancelled stale %s @ %d (%s)",
                    cfg.symbol, side, price, "price drift" if drift else "time stale",
                )

    async def _rebalance_grid(self, cfg: RealGridConfig, state: RealGridState):
        """Cancel all open orders and clear state for fresh grid placement.

        Called after daily reset to ensure orders are placed at current price levels
        rather than stale levels from previous day. Sells matched to filled buys
        are kept (Fix 5); an order whose cancel is not confirmed stays tracked.
        """
        logger.info("[RealGrid %s] Rebalancing grid — cancelling all open orders", cfg.symbol)

        for price, order_id in list(state.active_buys.items()):
            if await self._cancel_order(cfg.symbol, order_id):
                del state.active_buys[price]
                logger.debug("[RealGrid %s] Cancelled BUY @ %d", cfg.symbol, price)

        for price, order_id in list(state.active_sells.items()):
            if price in state.filled_buy_prices:
                logger.info(
                    "[RealGrid %s] Keeping SELL @ %d (matched to filled buy @ %d)",
                    cfg.symbol, price, price,
                )
                continue
            if state.active_sells.get(price) != order_id:
                continue  # removed earlier in this pass (superseded stop)
            if await self._cancel_order(cfg.symbol, order_id, close_journal=False):
                del state.active_sells[price]
                await self._after_confirmed_sell_cancel(cfg.symbol, state, price, order_id)
                logger.debug("[RealGrid %s] Cancelled SELL @ %d", cfg.symbol, price)

        logger.info("[RealGrid %s] Grid rebalanced — will place fresh orders at next tick", cfg.symbol)

    async def _cancel_order(self, symbol: str, order_id: str, client=None, close_journal: bool = True) -> bool:
        """Cancel an order via backend. Returns True when the backend confirmed it."""
        client = client or self._http
        try:
            resp = await client.post(
                f"{BACKEND_API_BASE}/api/trade/cancel-order",
                json={"symbol": symbol, "orderId": int(order_id)},
            )
            if resp.status_code != 200:
                logger.warning("Failed to cancel order %s: %s", order_id, resp.text)
                return False
            if not close_journal:
                return True
            # Close the journal entry so cancelled orders don't sit
            # in the journal as OPEN forever (they'd otherwise dominate
            # open_entries and make the stats meaningless)
            try:
                await self._journal.record_exit(order_id, 0.0, "CANCELLED", 0.0)
            except Exception as e:
                logger.warning("Journal exit (cancel) failed: %s", e)
            return True
        except Exception as e:
            logger.warning("Cancel order failed: %s", e)
            return False

    async def _update_pnl(self, cfg: RealGridConfig, state: RealGridState):
        """PnL is now calculated in _sync_open_orders() when fills are detected.
        This method is kept as a no-op to avoid double-counting."""
        pass

    async def _fetch_price(self, symbol: str) -> float:
        """Fetch current price from Binance TH public API (no key needed)."""
        try:
            # Binance TH uses /api/v1/ticker/price (not /api/v3 like Binance Global)
            resp = await self._http.get(
                f"{BINANCE_PUBLIC_REST}/api/v1/ticker/price",
                params={"symbol": symbol},
            )
            if resp.status_code == 200:
                price = float(resp.json()["price"])
                # Update risk manager price freshness
                self._risk.update_price_timestamp(symbol)
                return price
        except Exception as e:
            logger.warning("Failed to fetch price for %s: %s", symbol, e)
        return 0.0

    async def _calculate_atr_spacing(self, cfg: RealGridConfig, current_price: float) -> float:
        """Calculate dynamic grid spacing based on ATR (Average True Range).
        
        Uses klines data to calculate ATR, then converts to a percentage spacing.
        Falls back to fixed spacing if ATR calculation fails.
        
        Returns:
            Spacing percentage (clamped to min/max bounds)
        """
        try:
            # Fetch klines for ATR calculation (1h interval, need atr_period + 1 candles)
            resp = await self._http.get(
                f"{BINANCE_PUBLIC_REST}/api/v1/klines",
                params={
                    "symbol": cfg.symbol,
                    "interval": "1h",
                    "limit": cfg.atr_period + 1,
                },
            )
            if resp.status_code != 200:
                logger.debug("[%s] Failed to fetch klines for ATR, using fixed spacing", cfg.symbol)
                return cfg.grid_spacing_pct

            klines = resp.json()
            if len(klines) < cfg.atr_period + 1:
                return cfg.grid_spacing_pct

            # Calculate True Range for each candle
            true_ranges = []
            for i in range(1, len(klines)):
                high = float(klines[i][2])
                low = float(klines[i][3])
                prev_close = float(klines[i - 1][4])
                
                # True Range = max(high-low, abs(high-prev_close), abs(low-prev_close))
                tr = max(
                    high - low,
                    abs(high - prev_close),
                    abs(low - prev_close),
                )
                true_ranges.append(tr)

            # ATR = simple average of true ranges
            atr = sum(true_ranges[-cfg.atr_period:]) / cfg.atr_period
            
            # Convert ATR to percentage of current price
            atr_pct = (atr / current_price) * 100 * cfg.atr_multiplier
            
            # Clamp to min/max bounds
            spacing_pct = max(cfg.min_spacing_pct, min(cfg.max_spacing_pct, atr_pct))
            
            logger.debug(
                "[%s] ATR=%.2f (%.2f%%) -> spacing=%.2f%% (bounds: %.1f-%.1f%%)",
                cfg.symbol, atr, atr_pct, spacing_pct, cfg.min_spacing_pct, cfg.max_spacing_pct,
            )
            return spacing_pct

        except Exception as e:
            logger.debug("[%s] ATR calculation failed: %s, using fixed spacing", cfg.symbol, e)
            return cfg.grid_spacing_pct

    def _calculate_compound_order_size(self, cfg: RealGridConfig, state: RealGridState) -> float:
        """Calculate scaled order size based on accumulated profits (auto-compounding).
        
        Logic:
        - For every `compound_threshold_thb` of cumulative profit, increase order size by `compound_factor` (10%).
        - Cap at `max_compound_multiplier` times the base order size.
        - If cumulative_pnl is negative (losses), revert to base order size.
        
        Returns:
            Scaled order size (float)
        """
        base_size = cfg.order_size
        
        if not cfg.auto_compound_enabled or state.cumulative_pnl <= 0:
            return base_size
        
        # Calculate how many thresholds we've hit
        thresholds_hit = int(state.cumulative_pnl / cfg.compound_threshold_thb)
        
        if thresholds_hit <= 0:
            return base_size
        
        # Scale: 1 + (thresholds_hit * compound_factor), capped at max_compound_multiplier
        multiplier = min(1.0 + (thresholds_hit * cfg.compound_factor), cfg.max_compound_multiplier)
        scaled_size = base_size * multiplier
        
        return scaled_size

    def _auto_tune_compound_threshold(self, cfg: RealGridConfig, state: RealGridState):
        """Auto-tune compound threshold based on recent performance.
        
        Adjusts compound_threshold_thb dynamically:
        - If profit velocity is high (>100 THB/day): raise threshold (compound less aggressively)
        - If profit velocity is low (0-30 THB/day): lower threshold (compound more aggressively)
        - If losing money: reset to default
        """
        if not cfg.auto_compound_enabled:
            return
        
        velocity = state.profit_velocity_thb_per_day
        history = state.daily_pnl_history[-7:]  # last 7 days
        
        if not history:
            return
        
        avg_daily = sum(history) / len(history)
        old_threshold = cfg.compound_threshold_thb
        
        if velocity > 100 and avg_daily > 50:
            # Strong performance — raise threshold to compound less aggressively
            cfg.compound_threshold_thb = min(old_threshold * 1.2, 5000.0)
        elif velocity > 30 and avg_daily > 0:
            # Moderate performance — fine-tune upward slightly
            cfg.compound_threshold_thb = min(old_threshold * 1.05, 5000.0)
        elif velocity > 0 and avg_daily > 0:
            # Slow profit — lower threshold to compound more aggressively
            cfg.compound_threshold_thb = max(old_threshold * 0.9, 100.0)
        elif avg_daily <= 0 and len(history) >= 3:
            # Losing money — reset to default
            cfg.compound_threshold_thb = 500.0
        else:
            return  # not enough data
        
        if abs(cfg.compound_threshold_thb - old_threshold) > 1:
            logger.info(
                "[RealGrid %s] Auto-tune compound threshold: %.0f -> %.0f THB "
                "(velocity=%.1f THB/day, avg_daily=%.1f)",
                cfg.symbol, old_threshold, cfg.compound_threshold_thb,
                velocity, avg_daily,
            )

    # ── Regime Detection ─────────────────────────────────────────────────────

    async def _detect_regime(self, cfg: RealGridConfig, state: RealGridState) -> str:
        """Detect market regime using ATR percentile from historical data.
        
        Fetches 100 hourly candles, computes ATR for each window, then
        classifies current ATR into a percentile to determine regime:
        - low_vol:  ATR < 25th percentile → tighten grids, more fills
        - normal:   ATR 25th-75th → standard ATR-based spacing
        - high_vol: ATR 75th-90th → widen grids, avoid getting stuck
        - extreme:  ATR > 90th percentile → pause trading (danger zone)
        
        Returns:
            Regime string: "low_vol", "normal", "high_vol", "extreme"
        """
        try:
            # Fetch longer history for percentile calculation
            resp = await self._http.get(
                f"{BINANCE_PUBLIC_REST}/api/v1/klines",
                params={
                    "symbol": cfg.symbol,
                    "interval": "1h",
                    "limit": 100,  # ~4 days of hourly data
                },
            )
            if resp.status_code != 200 or len(resp.json()) < 30:
                return "normal"  # fallback if insufficient data

            klines = resp.json()
            
            # Calculate True Range for each candle
            true_ranges = []
            for i in range(1, len(klines)):
                high = float(klines[i][2])
                low = float(klines[i][3])
                prev_close = float(klines[i - 1][4])
                tr = max(high - low, abs(high - prev_close), abs(low - prev_close))
                true_ranges.append(tr)
            
            if len(true_ranges) < cfg.atr_period:
                return "normal"
            
            # Calculate rolling ATR values for percentile ranking
            atr_values = []
            for i in range(cfg.atr_period, len(true_ranges) + 1):
                window = true_ranges[i - cfg.atr_period:i]
                atr_values.append(sum(window) / cfg.atr_period)
            
            if not atr_values:
                return "normal"
            
            # Current ATR (most recent window)
            current_atr = atr_values[-1]
            
            # Calculate percentile: what % of historical ATR values are below current
            below_count = sum(1 for v in atr_values if v < current_atr)
            percentile = (below_count / len(atr_values)) * 100
            
            state.atr_percentile = percentile
            
            # Classify regime
            if percentile >= REGIME_THRESHOLDS["extreme"]:
                regime = "extreme"
            elif percentile >= REGIME_THRESHOLDS["high_vol"]:
                regime = "high_vol"
            elif percentile < REGIME_THRESHOLDS["low_vol"]:
                regime = "low_vol"
            else:
                regime = "normal"
            
            # Track regime history
            state.regime_history.append(regime)
            if len(state.regime_history) > 30:
                state.regime_history = state.regime_history[-30:]
            
            return regime
            
        except Exception as e:
            logger.debug("[%s] Regime detection failed: %s", cfg.symbol, e)
            return "normal"

    def _apply_regime_adjustment(self, cfg: RealGridConfig, state: RealGridState, regime: str):
        """Adjust grid parameters based on detected market regime.
        
        - low_vol:  tighten spacing (more fills), increase grid_levels
        - normal:   no adjustment
        - high_vol: widen spacing (fewer fills, avoid stuck positions)
        - extreme:  HALT trading (too dangerous)
        """
        state.regime = regime
        base_levels = SYMBOL_DEFAULTS.get(cfg.symbol, {}).get("grid_levels", 2)
        
        if regime == "low_vol":
            # Tighten: more levels, tighter spacing for more fills
            cfg.grid_levels = min(base_levels + 1, 5)
            cfg.grid_spacing_pct = max(1.0, cfg.grid_spacing_pct * 0.67)  # reduce to ~1%
            cfg.min_spacing_pct = max(MIN_PROFITABLE_SPACING_PCT, cfg.min_spacing_pct * 0.8)
            logger.info("[%s] LOW_VOL regime: levels=%d, spacing=%.1f%% (tighter for more fills)", cfg.symbol, cfg.grid_levels, cfg.grid_spacing_pct)
            
        elif regime == "high_vol":
            # Widen: fewer levels, wider spacing to avoid getting stuck
            cfg.grid_levels = max(base_levels - 1, 1)
            cfg.min_spacing_pct = cfg.min_spacing_pct * 1.5
            logger.info("[%s] HIGH_VOL regime: levels=%d, wider spacing", cfg.symbol, cfg.grid_levels)
            
        elif regime == "extreme":
            # HALT — too dangerous to trade
            state.halted = True
            logger.warning("[%s] EXTREME regime: TRADING HALTED (ATR >90th percentile)", cfg.symbol)
            
        else:  # normal
            cfg.grid_levels = base_levels
            # Reset to symbol defaults
            cfg.grid_spacing_pct = SYMBOL_DEFAULTS.get(cfg.symbol, {}).get("grid_spacing_pct", 1.5)
            cfg.min_spacing_pct = SYMBOL_DEFAULTS.get(cfg.symbol, {}).get("min_spacing_pct", 1.0)

    # ── Trend Detection (Bear Market Protection) ─────────────────────────────

    async def _detect_trend(self, cfg: RealGridConfig, state: RealGridState) -> str:
        """Detect market trend direction using SMA crossover.
        
        Uses 10-period and 30-period SMA on hourly candles:
        - bullish: SMA10 > SMA30 (uptrend)
        - neutral: SMA10 ≈ SMA30 (sideways)
        - bearish: SMA10 < SMA30 (downtrend)
        
        Returns:
            Trend string: "bullish", "neutral", "bearish"
        """
        try:
            resp = await self._http.get(
                f"{BINANCE_PUBLIC_REST}/api/v1/klines",
                params={
                    "symbol": cfg.symbol,
                    "interval": "1h",
                    "limit": 35,  # Need 30+ candles for SMA30
                },
            )
            if resp.status_code != 200 or len(resp.json()) < 30:
                return "neutral"

            klines = resp.json()
            closes = [float(k[4]) for k in klines]  # Close prices
            
            # Calculate SMA10 and SMA30
            sma10 = sum(closes[-10:]) / 10
            sma30 = sum(closes[-30:]) / 30
            
            # Determine trend with hysteresis (avoid flip-flopping)
            diff_pct = ((sma10 - sma30) / sma30) * 100
            
            if diff_pct > 0.5:  # SMA10 > SMA30 by 0.5%
                trend = "bullish"
            elif diff_pct < -0.5:  # SMA10 < SMA30 by 0.5%
                trend = "bearish"
            else:
                trend = "neutral"
            
            return trend
            
        except Exception as e:
            logger.debug("[%s] Trend detection failed: %s", cfg.symbol, e)
            return "neutral"

    async def _handle_bear_market(self, cfg: RealGridConfig, state: RealGridState, price: float) -> bool:
        """Handle bear market conditions.
        
        When trend is bearish:
        1. Cancel all open buy orders
        2. Set trend_paused flag to prevent new buy orders
        3. Log the action
        
        When trend recovers from bearish:
        1. Clear trend_paused flag
        2. Resume normal operation
        
        Returns:
            True if trading should be skipped (bear market active), False otherwise
        """
        trend = await self._detect_trend(cfg, state)
        state.trend = trend
        
        if trend == "bearish":
            # Cancel all open buy orders
            if state.active_buys:
                logger.warning(
                    "[RealGrid %s] BEAR MARKET detected: cancelling %d buy orders",
                    cfg.symbol, len(state.active_buys)
                )
                for price_key, order_id in list(state.active_buys.items()):
                    await self._cancel_order(cfg.symbol, order_id)
                    del state.active_buys[price_key]
            
            # Prevent new buy orders
            if not state.trend_paused:
                logger.warning(
                    "[RealGrid %s] BEAR MARKET: buy orders paused (SMA10 < SMA30)",
                    cfg.symbol
                )
            state.trend_paused = True
            return True  # Skip grid placement
        
        elif state.trend_paused and trend != "bearish":
            # Trend recovered — resume trading
            logger.info(
                "[RealGrid %s] Trend recovered to '%s': resuming buy orders",
                cfg.symbol, trend
            )
            state.trend_paused = False
        
        return False  # Continue normal operation

    async def _verify_fill(self, symbol: str, order_id: str) -> str:
        """Ask the exchange what actually happened to an order that vanished
        from the open-orders list.

        Returns:
            "filled"    — exchange confirms FILLED; safe to book PnL
            "cancelled" — CANCELED/EXPIRED/REJECTED (e.g. manual cancel in the
                          Binance app); must NOT be booked as a fill
            "open"      — still on the book (open-orders snapshot raced);
                          skip processing this tick
            "unknown"   — status query failed; caller decides the fallback
        """
        try:
            resp = await self._http.get(
                f"{BACKEND_API_BASE}/api/trade/order-status",
                params={"symbol": symbol, "orderId": order_id},
            )
            if resp.status_code == 200:
                status = (resp.json().get("status") or "").upper()
                if status == "FILLED":
                    return "filled"
                if status in ("CANCELED", "CANCELLED", "EXPIRED", "REJECTED"):
                    return "cancelled"
                if status in ("NEW", "PARTIALLY_FILLED"):
                    return "open"
        except Exception as e:
            logger.debug("[%s] order-status query failed for %s: %s", symbol, order_id, e)
        return "unknown"

    # Re-place an unfilled stop-loss sell when price falls this % below its
    # limit price. Below this, the limit is still near price and can fill on
    # a bounce; chasing too eagerly just locks in worse exits.
    STOP_LOSS_CHASE_PCT = 1.0

    async def _check_stop_loss(self, cfg: RealGridConfig, state: RealGridState, price: float):
        """Check if any filled buy positions have dropped below stop-loss threshold.

        When price drops > stop_loss_pct below a filled buy price, place a
        LIMIT SELL at current price to cut the loss. Unlike the original
        fire-and-forget version, this:
        1. Only marks the stop as triggered if the exchange ACCEPTED the order
           (placement can fail: rate limiter, insufficient balance, API error).
           Failed placements retry automatically on the next tick.
        2. Chases a falling market: if the placed stop sell is still unfilled
           and price has dropped a further STOP_LOSS_CHASE_PCT below its limit
           price, cancels and re-places it at the new current price.
        """
        if not state.filled_buy_prices or cfg.buy_only:
            return

        stop_threshold = cfg.stop_loss_pct / 100.0
        tick = cfg.tick_size

        for buy_price in list(state.filled_buy_prices.keys()):
            if state.stop_loss_triggered.get(buy_price, False):
                # Stop already placed — verify it's still working, chase if stale
                placed_price = state.stop_loss_orders.get(buy_price)
                if placed_price is None:
                    continue  # legacy state from before chase tracking existed
                if placed_price not in state.active_sells:
                    # Order left the book with an outcome we did not confirm
                    # here (it may have filled), so do not place a second
                    # stop. Confirmed cancellations re-arm the stop where they
                    # happen (_book_cancelled_sell).
                    state.stop_loss_orders.pop(buy_price, None)
                    continue
                # Chase: price fell well below our resting limit — it won't fill
                if price < placed_price * (1 - self.STOP_LOSS_CHASE_PCT / 100.0):
                    old_oid = state.active_sells.get(placed_price)
                    logger.warning(
                        "[RealGrid %s] STOP-LOSS chase: limit@%d unfilled, price now %d — re-placing",
                        cfg.symbol, int(placed_price), int(price),
                    )
                    if old_oid:
                        await self._cancel_order(cfg.symbol, old_oid)
                        state.order_times.pop(old_oid, None)
                    state.active_sells.pop(placed_price, None)
                    state.stop_loss_orders.pop(buy_price, None)
                    # Reset so the placement path below runs fresh this tick
                    state.stop_loss_triggered[buy_price] = False
                else:
                    continue

            drop_pct = (buy_price - price) / buy_price
            if drop_pct >= stop_threshold:
                sell_price = int(round(price / tick) * tick)

                logger.warning(
                    "[RealGrid %s] STOP-LOSS triggered: bought@%d, now@%d (drop=%.1f%%, threshold=%.1f%%)",
                    cfg.symbol, buy_price, sell_price, drop_pct * 100, cfg.stop_loss_pct
                )

                placed = await self._place_grid_order(
                    cfg, state, "SELL", sell_price, quantity=state.lot_qty.get(buy_price)
                )
                if not placed:
                    # Do NOT mark triggered — the position is still unprotected.
                    # Next tick retries. This was the original bug: a rate-limited
                    # or rejected stop order was marked "handled" forever.
                    logger.warning(
                        "[RealGrid %s] STOP-LOSS placement FAILED for buy@%d — will retry next tick",
                        cfg.symbol, buy_price,
                    )
                    continue

                state.stop_loss_triggered[buy_price] = True
                state.stop_loss_orders[buy_price] = float(sell_price)

                self._notifications.append({
                    "type": "stop_loss",
                    "side": "SELL",
                    "symbol": cfg.symbol,
                    "price": sell_price,
                    "quantity": state.current_order_size if state.current_order_size > 0 else cfg.order_size,
                    "profit": 0,
                    "trade_id": f"sl_{buy_price}",
                    "timestamp": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                    "message": f"{cfg.symbol} STOP-LOSS: bought@{buy_price} selling@{sell_price} (loss={drop_pct*100:.1f}%)",
                })
                await self._webhook.send_fill_alert(
                    symbol=cfg.symbol, side="SELL",
                    price=sell_price,
                    quantity=state.current_order_size if state.current_order_size > 0 else cfg.order_size,
                )

    # ── Capital Allocation Engine ─────────────────────────────────────────────

    def _calculate_sharpe(self, state: RealGridState) -> float:
        """Calculate Sharpe ratio from trade returns.
        
        Returns:
            Annualized Sharpe ratio, or 0.0 if insufficient data
        """
        if len(state.trade_returns) < 5:
            return 0.0
        import statistics as _stats
        mean_r = _stats.mean(state.trade_returns)
        std_r = _stats.stdev(state.trade_returns)
        if std_r == 0:
            return 0.0
        return (mean_r / std_r) * (252 ** 0.5)

    def _compute_allocation_scores(self) -> Dict[str, float]:
        """Compute composite allocation score for each symbol.
        
        Score = weighted combination of:
        - Sharpe ratio (risk-adjusted return) — weight 0.4
        - Fill rate (execution quality) — weight 0.3
        - Profit velocity (THB/day) — weight 0.3
        
        Returns:
            Dict of symbol -> score (normalized 0-1)
        """
        scores = {}
        
        for symbol, state in self.states.items():
            # Need minimum tracking data
            tracking_days = (time.time() - state.performance_start_time) / 86400 if state.performance_start_time > 0 else 0
            if tracking_days < MIN_ALLOCATION_TRACKING_DAYS:
                scores[symbol] = 0.5  # neutral score for new symbols
                continue
            
            # Sharpe component (normalized: -1 to +3 range -> 0 to 1)
            sharpe = self._calculate_sharpe(state)
            sharpe_norm = max(0.0, min(1.0, (sharpe + 1) / 4))
            
            # Fill rate component (already 0-1)
            fill_norm = state.fill_rate
            
            # Velocity component (normalized by total portfolio velocity)
            total_velocity = sum(s.profit_velocity_thb_per_day for s in self.states.values())
            if total_velocity > 0:
                vel_norm = state.profit_velocity_thb_per_day / total_velocity
            else:
                vel_norm = 0.5  # equal weight if no velocity
            
            # Weighted composite
            score = (0.4 * sharpe_norm) + (0.3 * fill_norm) + (0.3 * vel_norm)
            scores[symbol] = score
            state.allocation_score = score
        
        return scores

    def _run_capital_allocation(self):
        """Rebalance capital allocation across symbols based on performance.
        
        Adjusts grid_levels per symbol:
        - Top performers get more grid levels (more capital deployed)
        - Underperformers get fewer levels (less capital at risk)
        - Minimum 1 level always maintained (keep symbol active)
        
        Runs on daily reset after PnL recording.
        """
        scores = self._compute_allocation_scores()
        
        if not scores:
            return
        
        # Sort symbols by score
        ranked = sorted(scores.items(), key=lambda x: x[1], reverse=True)
        n_symbols = len(ranked)
        
        logger.info(
            "[CapitalAllocation] Rankings: %s",
            ", ".join(f"{s}={sc:.2f}" for s, sc in ranked),
        )
        
        # Allocate grid levels proportionally
        # Top symbol gets 2x base, bottom gets 0.5x, linear interpolation
        for rank, (symbol, score) in enumerate(ranked):
            cfg = next((c for c in self.configs if c.symbol == symbol), None)
            state = self.states.get(symbol)
            if not cfg or not state:
                continue
            
            base_levels = SYMBOL_DEFAULTS.get(symbol, {}).get("grid_levels", 2)
            
            # Linear interpolation: rank 0 (best) -> 1.5x, rank N-1 (worst) -> 0.5x
            if n_symbols > 1:
                weight = 1.5 - (rank / (n_symbols - 1))  # 1.5 to 0.5
            else:
                weight = 1.0
            
            # Apply: adjust grid_levels (min 1, max 5)
            new_levels = max(1, min(5, int(base_levels * weight)))
            
            # Only adjust if regime is normal (regime takes priority)
            if state.regime == "normal":
                old_levels = cfg.grid_levels
                cfg.grid_levels = new_levels
                state.allocation_weight = weight
                
                if old_levels != new_levels:
                    logger.info(
                        "[CapitalAllocation] %s: levels %d -> %d (weight=%.2f, score=%.2f)",
                        symbol, old_levels, new_levels, weight, score,
                    )

    # ── Fee-Aware Spacing Floor ──────────────────────────────────────────────

    def _apply_fee_floor(self, cfg: RealGridConfig, spacing_pct: float) -> float:
        """Ensure spacing exceeds minimum profitable threshold after fees.
        
        Binance TH charges 0.1% per trade (0.2% round trip).
        Grid spacing must be > 0.5% to ensure profit after fees.
        
        Args:
            cfg: Grid config (for symbol-specific min_spacing_pct)
            spacing_pct: Computed spacing percentage
        
        Returns:
            Adjusted spacing (floored to fee-aware minimum)
        """
        # Hard floor: must exceed fee threshold
        fee_floor = max(MIN_PROFITABLE_SPACING_PCT, cfg.min_spacing_pct)
        
        if spacing_pct < fee_floor:
            logger.debug(
                "[%s] Fee floor enforced: %.2f%% -> %.2f%% (min profitable: %.1f%%)",
                cfg.symbol, spacing_pct, fee_floor, MIN_PROFITABLE_SPACING_PCT,
            )
            return fee_floor
        
        return spacing_pct

    async def _send_comprehensive_digest(self):
        """Send comprehensive daily digest across all symbols via webhook."""
        if not self._webhook._enabled:
            return
        
        lines = ["📊 <b>Daily Trading Digest</b>", ""]
        total_pnl = 0.0
        total_trades = 0
        
        for symbol, state in self.states.items():
            pnl = state.daily_pnl
            trades = state.daily_trades
            total_pnl += pnl
            total_trades += trades
            
            emoji = "🟢" if pnl >= 0 else "🔴"
            cum_pnl = state.cumulative_pnl
            fill_pct = state.fill_rate * 100
            velocity = state.profit_velocity_thb_per_day
            
            lines.append(f"{emoji} <b>{symbol}</b>")
            lines.append(f"  PnL: {pnl:+.2f} THB | Trades: {trades}")
            lines.append(f"  Fill: {fill_pct:.1f}% | Vel: {velocity:.1f} THB/day")
            lines.append(f"  Cum PnL: {cum_pnl:+.2f} THB")
            lines.append(f"  Regime: {state.regime} | Alloc: {state.allocation_weight:.1f}x")
            
            # Sharpe/Sortino if enough data
            if len(state.trade_returns) >= 10:
                import statistics as _stats
                mean_r = _stats.mean(state.trade_returns)
                std_r = _stats.stdev(state.trade_returns)
                sharpe = (mean_r / std_r * (252 ** 0.5)) if std_r > 0 else 0
                lines.append(f"  Sharpe: {sharpe:.2f}")
            
            lines.append("")
        
        lines.append(f"💰 <b>Total PnL: {total_pnl:+.2f} THB</b>")
        lines.append(f"📈 Total Trades: {total_trades}")
        
        await self._webhook._queue.put("\n".join(lines))

    async def _fetch_balances(self) -> Dict[str, float]:
        """Fetch account balances from backend."""
        try:
            resp = await self._http.get(f"{BACKEND_API_BASE}/api/trade/balances")
            if resp.status_code == 200:
                data = resp.json()
                return {b["currency"]: float(b["free"]) for b in data.get("balances", [])}
        except Exception as e:
            logger.warning("Failed to fetch balances: %s", e)
        return {}

    async def _account_value_quote(self, symbol: str, price: float) -> float:
        """Free quote balance plus free base balance at ``price``, in quote units.

        For a THB pair this is the THB value the pair can trade with. Returns
        0.0 when balances cannot be fetched or the symbol format is unknown.
        """
        for quote in ["THB", "USDT", "BUSD", "BTC"]:
            if symbol.endswith(quote):
                base_asset = symbol[: -len(quote)]
                break
        else:
            return 0.0
        balances = await self._fetch_balances()
        return balances.get(quote, 0.0) + balances.get(base_asset, 0.0) * price

    async def _can_place_sell(self, symbol: str, qty: float) -> bool:
        """Check if we have enough base asset to place a SELL order."""
        # Extract base asset from symbol (e.g., ETHTHB -> ETH, BTCTHB -> BTC)
        for quote in ["THB", "USDT", "BUSD", "BTC"]:
            if symbol.endswith(quote):
                base_asset = symbol[: -len(quote)]
                break
        else:
            return True  # Unknown symbol format, allow

        balances = await self._fetch_balances()
        available = balances.get(base_asset, 0.0)
        if available < qty:
            logger.debug(
                "[RealGrid %s] Skipping SELL: need %.6f %s but have %.6f",
                symbol, qty, base_asset, available,
            )
            return False
        return True

    async def _can_place_buy(self, symbol: str, qty: float, price: int) -> bool:
        """Check if we have enough quote currency to place a BUY order.
        
        Required = qty * price (notional value).
        Also enforces MIN_FREE_RESERVE_THB to prevent capital starvation
        for DCA/trend bots.
        """
        MIN_FREE_RESERVE_THB = 30.0  # Safety buffer — total capital ~555 THB, can't afford 200 reserve
        # Extract quote asset from symbol (e.g., ETHTHB -> THB, BTCTHB -> THB)
        for quote in ["THB", "USDT", "BUSD", "BTC"]:
            if symbol.endswith(quote):
                quote_asset = quote
                break
        else:
            return True  # Unknown symbol format, allow

        balances = await self._fetch_balances()
        available = balances.get(quote_asset, 0.0)
        required = qty * price  # Notional value needed
        
        logger.info(
            "[RealGrid %s] Reserve check: available=%.2f %s, required=%.2f, reserve=%.0f",
            symbol, available, quote_asset, required, MIN_FREE_RESERVE_THB,
        )
        
        if available < required:
            logger.warning(
                "[RealGrid %s] Skipping BUY: need %.2f %s but have %.2f",
                symbol, required, quote_asset, available,
            )
            return False
        # Capital reserve check: keep MIN_FREE_RESERVE_THB free for other bots
        if quote_asset == "THB" and (available - required) < MIN_FREE_RESERVE_THB:
            logger.warning(
                "[RealGrid %s] Skipping BUY: would leave only %.2f THB free (reserve: %.0f THB)",
                symbol, available - required, MIN_FREE_RESERVE_THB,
            )
            return False
        
        logger.info(
            "[RealGrid %s] Reserve check PASSED: will place BUY order",
            symbol,
        )
        return True

    async def _place_grid_order(
        self, cfg: RealGridConfig, state: RealGridState, side: str, price: float, quantity: Optional[float] = None
    ) -> bool:
        """Place a real LIMIT order at a grid level via Go backend.

        Returns True only when the exchange accepted the order — callers that
        must know the order exists (e.g. stop-loss) depend on this.
        """
        # ── Testnet safety: block ALL real order placement ──
        if not BINANCE_TH_MAINNET:
            logger.info(
                "[RealGrid %s] Order SKIPPED (testnet mode): %s @ %.2f",
                cfg.symbol, side, price,
            )
            return False

        self._inflight_orders = getattr(self, "_inflight_orders", 0) + 1
        try:
            return await self._place_grid_order_inner(cfg, state, side, price, quantity)
        finally:
            self._inflight_orders -= 1

    async def _place_grid_order_inner(
        self, cfg: RealGridConfig, state: RealGridState, side: str, price: float, quantity: Optional[float] = None
    ) -> bool:
        # Use dynamic order size from auto-compounding if available, else base config size
        order_size = state.current_order_size if state.current_order_size > 0 else cfg.order_size
        if quantity is not None and quantity > 0:
            order_size = quantity  # e.g. a stop-loss sell for a partly filled lot

        # Global guards apply before any balance check or backend request.  A
        # BUY adds exposure; a SELL remains available so a full portfolio can
        # still reduce exposure and exit positions.
        if side == "BUY":
            total_open_orders = self._total_open_orders()
            max_open_orders = self._risk.config.max_open_orders
            if total_open_orders >= max_open_orders:
                logger.info(
                    "[RealGrid %s] BUY blocked: global open-order cap %d/%d",
                    cfg.symbol,
                    total_open_orders,
                    max_open_orders,
                )
                return False

            total_open_notional = self._total_open_notional()
            candidate_notional = order_size * price
            if total_open_notional + candidate_notional > GRID_MAX_PORTFOLIO_NOTIONAL_THB:
                logger.info(
                    "[RealGrid %s] BUY blocked: portfolio notional %.2f + %.2f "
                    "> %.2f THB",
                    cfg.symbol,
                    total_open_notional,
                    candidate_notional,
                    GRID_MAX_PORTFOLIO_NOTIONAL_THB,
                )
                return False

        # ── Balance pre-checks ──
        if side == "SELL":
            if not await self._can_place_sell(cfg.symbol, order_size):
                logger.info("[RealGrid %s] SELL skipped: insufficient base balance", cfg.symbol)
                return False
        elif side == "BUY":
            if not await self._can_place_buy(cfg.symbol, order_size, price):
                logger.info("[RealGrid %s] BUY skipped: insufficient quote balance", cfg.symbol)
                return False

        # ── Risk check before order ──
        # A BUY adds exposure, so it is sized against the account value
        # (risk_per_trade_pct). An unknown balance blocks the BUY rather than
        # skipping the check. A SELL only reduces exposure and stays exempt.
        risk_kwargs = {}
        if side == "BUY":
            account_value = await self._account_value_quote(cfg.symbol, price)
            if account_value <= 0:
                logger.info("[RealGrid %s] BUY blocked: account value unknown", cfg.symbol)
                return False
            risk_kwargs["current_balance_thb"] = account_value
        allowed, reason = self._risk.check_order_allowed(
            symbol=cfg.symbol,
            side=side,
            quantity=order_size,
            price=price,
            **risk_kwargs,
        )
        if not allowed:
            logger.info("[RealGrid %s] Order blocked by risk: %s", cfg.symbol, reason)
            return False

        # A kill may have landed while this placement awaited balances or the
        # risk check; never submit after it.
        if not self._enabled:
            logger.warning("[RealGrid %s] %s @ %.2f dropped: bot killed mid-placement", cfg.symbol, side, price)
            return False

        try:
            resp = await self._http.post(
                f"{BACKEND_API_BASE}/api/trade/order",
                json={
                    "symbol": cfg.symbol,
                    "side": side,
                    "quantity": order_size,
                    "price": price,  # >0 = LIMIT order
                },
            )
            if resp.status_code == 201:
                data = resp.json()
                order = data.get("order", {})
                order_id = str(order.get("orderId", ""))

                state.trades_executed += 1
                state.daily_trades += 1
                state.orders_placed += 1  # Performance: track orders placed
                # Update fill rate
                if state.orders_placed > 0:
                    state.fill_rate = state.orders_filled / state.orders_placed

                if side == "BUY":
                    state.active_buys[price] = order_id
                else:
                    state.active_sells[price] = order_id

                # Track order placement time for time-based stale cancellation
                state.order_times[order_id] = time.time()

                # Record in risk manager
                self._risk.record_order_placed(cfg.symbol)

                # Record in trade journal
                notional = order_size * price
                await self._journal.record_entry(JournalEntry(
                    symbol=cfg.symbol,
                    side=side,
                    strategy="grid_bot_v2",
                    entry_reason=f"Grid {side.lower()} @ level price={price}, spacing={cfg.grid_spacing_pct}%",
                    entry_price=price,
                    quantity=order_size,
                    expected_risk_thb=notional * 0.02,  # 2% risk estimate
                    expected_reward_thb=notional * (cfg.grid_spacing_pct / 100),
                    exchange_order_id=order_id,
                ))

                logger.info(
                    "[RealGrid %s] %s LIMIT @ %.2f placed (id=%s)",
                    cfg.symbol, side, price, order_id[:16] if order_id else "?",
                )
                return True
            elif resp.status_code == 400:
                # Insufficient balance or exchange rejection — log once then skip silently
                err = resp.json().get("error", "unknown")
                if "insufficient" in err.lower():
                    logger.warning(
                        "[RealGrid %s] %s @ %.2f rejected: insufficient balance. "
                        "Will retry next tick when balance may have changed.",
                        cfg.symbol, side, price,
                    )
                else:
                    logger.info("Order rejected @ %.2f: %s", price, err)
            elif resp.status_code >= 500:
                # The backend could not tell whether the exchange took it.
                self._note_uncertain_submission(cfg.symbol, side, price, f"HTTP {resp.status_code}")
            else:
                err = resp.json().get("error", "unknown")
                logger.info("Order rejected @ %.2f (HTTP %d): %s", price, resp.status_code, err)
        except Exception as e:
            # The request may have reached the exchange before it failed.
            self._note_uncertain_submission(cfg.symbol, side, price, str(e))
        return False

    def _note_uncertain_submission(self, symbol: str, side: str, price: float, why: str):
        logger.error(
            "[RealGrid %s] %s @ %.2f outcome unknown (%s): it may be live on the exchange",
            symbol, side, price, why,
        )
        self._uncertain_submissions.append({"symbol": symbol, "side": side, "price": price, "at": time.time()})


    def get_status(self) -> Dict:
        """Get current real grid bot status (includes risk metrics and auto-compound info)."""
        risk = get_risk_manager()
        journal = get_trade_journal()
        
        # Build per-symbol config lookup for compound settings
        cfg_lookup = {cfg.symbol: cfg for cfg in self.configs}
        
        symbols_data = {}
        total_cumulative_pnl = 0.0
        
        for symbol, state in self.states.items():
            cfg = cfg_lookup.get(symbol)
            current_size = state.current_order_size if state.current_order_size > 0 else (cfg.order_size if cfg else 0)
            base_size = cfg.order_size if cfg else 0
            compound_multiplier = current_size / base_size if base_size > 0 else 1.0
            
            symbols_data[symbol] = {
                "last_price": state.last_price,
                "active_buys": len(state.active_buys),
                "active_sells": len(state.active_sells),
                "trades_executed": state.trades_executed,
                "daily_pnl": round(state.daily_pnl, 2),
                "daily_trades": state.daily_trades,
                "halted": state.halted,
                # Auto-compounding metrics
                "cumulative_pnl": round(state.cumulative_pnl, 2),
                "current_order_size": round(current_size, 8),
                "base_order_size": round(base_size, 8),
                "compound_multiplier": round(compound_multiplier, 2),
            }
            total_cumulative_pnl += state.cumulative_pnl
        
        return {
            "running": self._running,
            "enabled": self._enabled,
            "symbols": symbols_data,
            "portfolio": {
                "open_orders": self._total_open_orders(),
                "max_open_orders": risk.config.max_open_orders,
                "open_notional_thb": round(self._total_open_notional(), 4),
                "max_notional_thb": GRID_MAX_PORTFOLIO_NOTIONAL_THB,
            },
            "total_cumulative_pnl": round(total_cumulative_pnl, 2),
            "risk": risk.get_status(),
            "journal_stats": journal.get_stats(),
        }

    def get_health(self) -> Dict:
        """Get health status with stuck detection.
        
        Returns health status including:
        - running: whether the bot loop is active
        - enabled: whether trading is enabled (not kill-switched)
        - last_tick_age_sec: how long since last tick per symbol
        - stuck: whether any symbol appears stuck (>5 min without tick)
        """
        now = time.time()
        stuck_threshold = 300  # 5 minutes
        
        last_tick_age = {}
        stuck_symbols = []
        for symbol in self.states:
            last_tick = self._last_tick_time.get(symbol, 0)
            age = now - last_tick if last_tick > 0 else -1
            last_tick_age[symbol] = round(age, 1)
            if age > stuck_threshold or age < 0:
                stuck_symbols.append(symbol)
        
        return {
            "healthy": self._running and self._enabled and len(stuck_symbols) == 0,
            "running": self._running,
            "enabled": self._enabled,
            "last_tick_age_sec": last_tick_age,
            "stuck_symbols": stuck_symbols,
        }

    def get_performance(self) -> Dict:
        """Get performance metrics for all symbols.
        
        Returns per-symbol performance data including:
        - fill rates, ATR spacing history, profit velocity
        - Recommendations for compound parameter tuning
        """
        cfg_lookup = {cfg.symbol: cfg for cfg in self.configs}
        per_symbol = {}
        
        for symbol, state in self.states.items():
            cfg = cfg_lookup.get(symbol)
            
            # ATR spacing analysis
            atr_hist = state.atr_spacing_history
            atr_avg = sum(atr_hist) / len(atr_hist) if atr_hist else 0.0
            atr_min = min(atr_hist) if atr_hist else 0.0
            atr_max = max(atr_hist) if atr_hist else 0.0
            atr_stddev = 0.0
            if len(atr_hist) > 1:
                mean = atr_avg
                atr_stddev = (sum((x - mean) ** 2 for x in atr_hist) / len(atr_hist)) ** 0.5
            
            # Profit velocity analysis
            velocity = state.profit_velocity_thb_per_day
            
            # Compound tuning recommendation
            recommendation = "hold"
            if cfg and cfg.auto_compound_enabled:
                if velocity > 100:  # earning >100 THB/day
                    recommendation = "increase_threshold"  # compound less aggressively
                elif velocity > 30:
                    recommendation = "hold"  # current settings good
                elif velocity > 0:
                    recommendation = "decrease_threshold"  # compound more aggressively
                else:
                    recommendation = "pause_compounding"  # no profit yet
            
            # Sharpe/Sortino risk-adjusted return metrics
            sharpe = sortino = 0.0
            if len(state.trade_returns) > 1:
                import statistics as _stats
                mean_r = _stats.mean(state.trade_returns)
                std_r = _stats.stdev(state.trade_returns)
                sharpe = (mean_r / std_r * (252 ** 0.5)) if std_r > 0 else 0.0
                neg_returns = [r for r in state.trade_returns if r < 0]
                std_neg = _stats.stdev(neg_returns) if len(neg_returns) > 1 else std_r
                sortino = (mean_r / std_neg * (252 ** 0.5)) if std_neg > 0 else 0.0
            
            per_symbol[symbol] = {
                # Fill rate metrics
                "orders_placed": state.orders_placed,
                "orders_filled": state.orders_filled,
                "fill_rate": round(state.fill_rate * 100, 2),  # as percentage
                "last_fill_timestamp": state.last_fill_timestamp,
                "last_fill_age_sec": round(time.time() - state.last_fill_timestamp, 0) if state.last_fill_timestamp > 0 else None,
                # ATR spacing metrics
                "last_atr_spacing_pct": round(state.last_atr_spacing_pct, 4),
                "atr_spacing_avg": round(atr_avg, 4),
                "atr_spacing_min": round(atr_min, 4),
                "atr_spacing_max": round(atr_max, 4),
                "atr_spacing_stddev": round(atr_stddev, 4),
                "atr_spacing_samples": len(atr_hist),
                "atr_config": {
                    "atr_period": cfg.atr_period if cfg else 14,
                    "atr_multiplier": cfg.atr_multiplier if cfg else 1.5,
                    "min_spacing_pct": cfg.min_spacing_pct if cfg else 0.5,
                    "max_spacing_pct": cfg.max_spacing_pct if cfg else 5.0,
                } if cfg else {},
                # Profit velocity
                "profit_velocity_thb_per_day": round(velocity, 2),
                "cumulative_pnl": round(state.cumulative_pnl, 2),
                "performance_tracking_days": round(
                    (time.time() - state.performance_start_time) / 86400, 2
                ) if state.performance_start_time > 0 else 0,
                # Risk-adjusted returns (Sharpe/Sortino)
                "sharpe_ratio": round(sharpe, 2),
                "sortino_ratio": round(sortino, 2),
                "trade_return_samples": len(state.trade_returns),
                # Compound tuning
                "compound_recommendation": recommendation,
                "current_compound_multiplier": round(
                    (state.current_order_size / cfg.order_size) if cfg and cfg.order_size > 0 else 1.0, 2
                ),
                "auto_tuned_compound_threshold": round(cfg.compound_threshold_thb, 2) if cfg else 500.0,
                # Regime detection & capital allocation
                "regime": state.regime,
                "atr_percentile": round(state.atr_percentile, 1),
                "allocation_weight": round(state.allocation_weight, 2),
                "allocation_score": round(state.allocation_score, 3),
                "current_grid_levels": cfg.grid_levels if cfg else 2,
            }
        
        # Cross-symbol comparison summary
        symbols_list = list(per_symbol.values())
        avg_fill_rate = sum(s["fill_rate"] for s in symbols_list) / len(symbols_list) if symbols_list else 0
        best_symbol = max(per_symbol.items(), key=lambda x: x[1]["fill_rate"]) if per_symbol else (None, None)
        worst_symbol = min(per_symbol.items(), key=lambda x: x[1]["fill_rate"]) if per_symbol else (None, None)
        
        return {
            "symbols": per_symbol,
            "summary": {
                "avg_fill_rate": round(avg_fill_rate, 2),
                "best_fill_symbol": best_symbol[0] if best_symbol[0] else None,
                "worst_fill_symbol": worst_symbol[0] if worst_symbol[0] else None,
                "total_profit_velocity": round(
                    sum(s["profit_velocity_thb_per_day"] for s in symbols_list), 2
                ),
                "avg_sharpe_ratio": round(
                    sum(s["sharpe_ratio"] for s in symbols_list) / len(symbols_list), 2
                ) if symbols_list else 0.0,
                "avg_sortino_ratio": round(
                    sum(s["sortino_ratio"] for s in symbols_list) / len(symbols_list), 2
                ) if symbols_list else 0.0,
            },
        }

    async def force_restart(self) -> bool:
        """Force restart the bot by resetting state and re-syncing.
        
        This is a recovery mechanism for when the bot gets stuck.
        Returns True if restart was successful.
        """
        logger.warning("Force restarting Real Grid Bot")
        try:
            # Clear stuck detection state
            self._last_tick_time.clear()
            # Re-sync open orders for all symbols
            for cfg in self.configs:
                state = self.states.get(cfg.symbol)
                if state:
                    await self._sync_open_orders(cfg, state)
                    logger.info("Re-synced open orders for %s", cfg.symbol)
            return True
        except Exception as e:
            logger.error("Force restart failed: %s", e)
            return False

    def get_notifications(self, limit: int = 20) -> List[dict]:
        """Get recent notifications (most recent first)."""
        return list(reversed(list(self._notifications)))[:limit]


# ── Singleton instance for FastAPI integration ────────────────────────────────

_real_grid_bot: Optional[RealGridBot] = None


def get_real_grid_bot() -> RealGridBot:
    global _real_grid_bot
    if _real_grid_bot is None:
        _real_grid_bot = RealGridBot()
    return _real_grid_bot
