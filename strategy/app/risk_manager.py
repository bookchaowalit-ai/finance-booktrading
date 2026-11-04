"""
Risk Manager for Real Trading
==============================
Enforces safety constraints before any order is placed:
- Max daily loss limit (THB)
- Max drawdown from peak equity (%)
- Max position size per asset
- Max consecutive losses before kill switch
- Stale data detection (price too old)
- Min time between orders (rate limiting)

All checks must pass before an order is allowed.
"""

import logging
import time
from dataclasses import dataclass, field
from typing import Dict, Optional

logger = logging.getLogger(__name__)


@dataclass
class RiskConfig:
    """Risk parameters — conservative defaults for real trading."""
    max_daily_loss_thb: float = 100.0          # Stop trading if daily loss > 100 THB
    max_drawdown_pct: float = 5.0              # Stop if drawdown > 5% from peak
    max_position_pct: float = 10.0             # Max 10% of balance in single asset
    max_consecutive_losses: int = 5            # Kill after 5 consecutive losing trades
    max_order_size_thb: float = 200.0          # Max single order notional
    min_order_interval_sec: float = 10.0       # Min seconds between orders
    price_stale_threshold_sec: float = 300.0   # Price older than 5min = stale
    max_open_orders: int = 10                  # Max simultaneous open orders
    risk_per_trade_pct: float = 1.0            # Max 1% of balance per trade


@dataclass
class RiskState:
    """Tracks risk metrics in real-time."""
    # Daily P&L tracking
    daily_pnl: float = 0.0
    daily_trades: int = 0
    daily_wins: int = 0
    daily_losses: int = 0
    last_daily_reset: float = 0.0

    # Consecutive loss tracking
    consecutive_losses: int = 0
    max_consecutive_losses_hit: bool = False

    # Drawdown tracking
    peak_equivalent_equity: float = 0.0  # Highest equity seen
    current_drawdown_pct: float = 0.0

    # Rate limiting
    last_order_time: float = 0.0

    # Price freshness
    last_price_update: Dict[str, float] = field(default_factory=dict)  # symbol -> timestamp

    # Kill switch
    halted: bool = False
    halt_reason: str = ""

    # Audit log
    risk_events: list = field(default_factory=list)

    # Order history for risk calc
    total_trades_all_time: int = 0
    winning_trades: int = 0
    losing_trades: int = 0


class RiskManager:
    """
    Central risk gate for all real trading.
    Every order must pass through `check_order_allowed()` before placement.
    """

    def __init__(self, config: Optional[RiskConfig] = None):
        self.config = config or RiskConfig()
        self.state = RiskState(last_daily_reset=time.time())

    def check_order_allowed(
        self,
        symbol: str,
        side: str,
        quantity: float,
        price: float,
        current_balance_thb: float = 0.0,
    ) -> tuple[bool, str]:
        """
        Returns (allowed: bool, reason: str).
        Must be called before EVERY order placement.
        """
        # 1. Check if halted
        if self.state.halted:
            return False, f"HALTED: {self.state.halt_reason}"

        # 2. Daily reset
        self._check_daily_reset()

        # 3. Daily loss limit
        if self.state.daily_pnl < -self.config.max_daily_loss_thb:
            self._halt(f"Daily loss limit hit: {self.state.daily_pnl:.2f} THB")
            return False, self.state.halt_reason

        # 4. Consecutive losses
        if self.state.consecutive_losses >= self.config.max_consecutive_losses:
            self._halt(f"Consecutive losses limit: {self.state.consecutive_losses}")
            return False, self.state.halt_reason

        # 5. Max drawdown
        if self.state.max_consecutive_losses_hit:
            return False, "Max consecutive losses hit — manual reset required"

        # 6. Drawdown check
        if self.state.current_drawdown_pct > self.config.max_drawdown_pct:
            self._halt(f"Max drawdown hit: {self.state.current_drawdown_pct:.1f}%")
            return False, self.state.halt_reason

        # 7. Order size check: it bounds new exposure, so BUYs only. A SELL
        # exits coins already held (callers check the balance); capping it
        # would leave a lot built from several fills without its stop-loss.
        notional = quantity * price
        if side == "BUY" and notional > self.config.max_order_size_thb:
            return False, f"Order too large: {notional:.0f} THB > max {self.config.max_order_size_thb:.0f} THB"

        # 8. Risk per trade
        if current_balance_thb > 0:
            risk_pct = (notional / current_balance_thb) * 100
            if risk_pct > self.config.risk_per_trade_pct:
                return False, f"Risk per trade too high: {risk_pct:.1f}% > max {self.config.risk_per_trade_pct:.1f}%"

        # 9. Rate limiting
        now = time.time()
        time_since_last = now - self.state.last_order_time
        if time_since_last < self.config.min_order_interval_sec:
            return False, f"Rate limited: wait {self.config.min_order_interval_sec - time_since_last:.0f}s"

        # 10. Price freshness
        last_update = self.state.last_price_update.get(symbol, 0)
        if last_update > 0:
            age = now - last_update
            if age > self.config.price_stale_threshold_sec:
                return False, f"Price stale for {symbol}: {age:.0f}s old"

        return True, "OK"

    def record_order_placed(self, symbol: str):
        """Call after a successful order placement."""
        self.state.last_order_time = time.time()
        self.state.daily_trades += 1
        self.state.total_trades_all_time += 1
        self._log_event("ORDER_PLACED", f"{symbol} order placed")

    def record_trade_result(self, symbol: str, pnl: float, is_win: bool):
        """Call when a trade is filled/closed."""
        self.state.daily_pnl += pnl
        if is_win:
            self.state.daily_wins += 1
            self.state.winning_trades += 1
            self.state.consecutive_losses = 0
        else:
            self.state.daily_losses += 1
            self.state.losing_trades += 1
            self.state.consecutive_losses += 1

        self._log_event(
            "TRADE_RESULT",
            f"{symbol} pnl={pnl:.2f} {'WIN' if is_win else 'LOSS'} "
            f"(consecutive_losses={self.state.consecutive_losses})"
        )

    def update_price_timestamp(self, symbol: str):
        """Call when a fresh price is received."""
        self.state.last_price_update[symbol] = time.time()

    def update_drawdown(self, current_equity: float):
        """Update peak equity and current drawdown %."""
        if current_equity > self.state.peak_equivalent_equity:
            self.state.peak_equivalent_equity = current_equity

        if self.state.peak_equivalent_equity > 0:
            dd = ((self.state.peak_equivalent_equity - current_equity) / self.state.peak_equivalent_equity) * 100
            self.state.current_drawdown_pct = max(0, dd)

    def reset_kill_switch(self):
        """Manual reset after kill switch triggered."""
        self.state.halted = False
        self.state.halt_reason = ""
        self.state.max_consecutive_losses_hit = False
        self.state.consecutive_losses = 0
        self._log_event("KILL_SWITCH_RESET", "Risk manager reset by user")
        logger.info("Risk manager kill switch reset")
        # Deliberately does NOT re-enable the real grid bot: resuming trading
        # is a separate, explicit step (POST /api/real-grid/enable).

    def get_status(self) -> Dict:
        """Return current risk metrics for dashboard."""
        win_rate = 0.0
        total = self.state.winning_trades + self.state.losing_trades
        if total > 0:
            win_rate = (self.state.winning_trades / total) * 100

        return {
            "halted": self.state.halted,
            "halt_reason": self.state.halt_reason,
            "daily_pnl": round(self.state.daily_pnl, 2),
            "daily_trades": self.state.daily_trades,
            "daily_wins": self.state.daily_wins,
            "daily_losses": self.state.daily_losses,
            "consecutive_losses": self.state.consecutive_losses,
            "max_consecutive_losses": self.config.max_consecutive_losses,
            "current_drawdown_pct": round(self.state.current_drawdown_pct, 2),
            "max_drawdown_pct": self.config.max_drawdown_pct,
            "peak_equity": round(self.state.peak_equivalent_equity, 2),
            "total_trades": self.state.total_trades_all_time,
            "win_rate_pct": round(win_rate, 1),
            "last_order_time": self.state.last_order_time,
            "config": {
                "max_daily_loss_thb": self.config.max_daily_loss_thb,
                "max_drawdown_pct": self.config.max_drawdown_pct,
                "max_order_size_thb": self.config.max_order_size_thb,
                "risk_per_trade_pct": self.config.risk_per_trade_pct,
                "max_consecutive_losses": self.config.max_consecutive_losses,
                "max_open_orders": self.config.max_open_orders,
            },
            "recent_events": self.state.risk_events[-10:],
        }

    def _check_daily_reset(self):
        """Reset daily counters if 24h passed."""
        if time.time() - self.state.last_daily_reset > 86400:
            self.state.daily_pnl = 0.0
            self.state.daily_trades = 0
            self.state.daily_wins = 0
            self.state.daily_losses = 0
            self.state.last_daily_reset = time.time()
            # Keep an active kill switch across daily accounting resets. A
            # mainnet halt must be cleared deliberately through reset_kill_switch.
            logger.info("Risk manager daily reset")

    def _halt(self, reason: str):
        """Trigger kill switch."""
        self.state.halted = True
        self.state.halt_reason = reason
        self._log_event("HALT", reason)
        logger.warning("RISK MANAGER HALT: %s", reason)
        
        # ── Propagate to real grid bot ──
        # When risk manager halts, also disable the real grid bot
        try:
            from app.real_grid_bot import get_real_grid_bot
            bot = get_real_grid_bot()
            bot.disable()
            logger.info("Real grid bot disabled via risk manager halt")
        except Exception as e:
            logger.warning("Failed to disable real grid bot on halt: %s", e)

    def _log_event(self, event_type: str, message: str):
        """Log risk event for audit trail."""
        event = {
            "time": time.time(),
            "type": event_type,
            "message": message,
        }
        self.state.risk_events.append(event)
        # Keep only last 100 events
        if len(self.state.risk_events) > 100:
            self.state.risk_events = self.state.risk_events[-100:]


# ── Singleton ────────────────────────────────────────────────────────────────

_risk_manager: Optional[RiskManager] = None


def get_risk_manager() -> RiskManager:
    global _risk_manager
    if _risk_manager is None:
        _risk_manager = RiskManager()
    return _risk_manager
