package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"trading-bot-system/backend/internal/adapter/exchange"
	"trading-bot-system/backend/internal/adapter/exchange/bitkub"
	"trading-bot-system/backend/internal/config"
	"trading-bot-system/backend/internal/domain/model"
	"trading-bot-system/backend/internal/logger"
)

// Grid order reconciliation.
//
// An order submission can time out after the exchange accepted it. Treating
// that as a failure and re-arming the level would double-submit. Instead:
//
//   - every grid order carries a client order ID;
//   - a submission whose outcome is unknown (timeout, transport error, 5xx,
//     unparsable 200) becomes the book's pending order;
//   - an accepted order that is not filled yet (NEW / PARTIALLY_FILLED) is
//     pending too;
//   - while an order is pending the grid places nothing. Each tick looks the
//     order up by client order ID first: FILLED records the fill,
//     CANCELED/EXPIRED/REJECTED records any partial fill and clears it, and
//     "not found" clears it only after gridUnknownGrace, because a request
//     that is still in flight can be unknown to the exchange for a moment;
//   - if the exchange cannot look orders up, the order stays pending and the
//     grid stops placing orders until an operator reconciles and restarts it.
//     Binance and Binance TH look up by client order ID; Bitkub scans open
//     orders and the fills made since submission (it has no direct query), and
//     a lookup it cannot decide keeps the order pending.

// gridOrderMaxAge is how long an accepted grid order may stay open before
// the grid cancels it. While it is open the grid places nothing, so a limit
// order the market has moved away from would otherwise block the grid. The
// cancel is only a request: the order stays pending until a lookup reports
// it final, so a fill that races the cancel is still recorded.
const gridOrderMaxAge = 15 * time.Minute

// gridCancelRetry is the minimum time between two cancel requests for the
// same order.
const gridCancelRetry = time.Minute

// gridUnknownGrace is how long an unknown order must be absent from the
// exchange before the grid treats it as never placed.
const gridUnknownGrace = 60 * time.Second

type gridOrderState int

const (
	gridOrderOpen     gridOrderState = iota // accepted, not final
	gridOrderFilled                         // fully filled
	gridOrderDone                           // final without a full fill
	gridOrderNotFound                       // the exchange has no such order
)

// gridOrderReport is the grid's view of one order.
type gridOrderReport struct {
	state       gridOrderState
	executedQty float64
	// provider is the exchange place sent the order to, also set with an
	// unknown-outcome error; "" means the current provider.
	provider string
}

// gridExchange is what the grid needs from an exchange.
type gridExchange interface {
	// place submits an order. An error for which exchange.IsOrderStateUnknown
	// is true means the order may exist.
	place(ctx context.Context, clientOrderID, side string, quantity, price float64) (gridOrderReport, error)
	// lookup returns the state of an order by client order ID. submittedAt
	// bounds the search on exchanges that cannot query by client order ID.
	// provider is the exchange the order went to ("" = current provider).
	lookup(ctx context.Context, provider, clientOrderID string, submittedAt time.Time) (gridOrderReport, error)
	// cancel requests the cancellation of an open order. A nil error does
	// not mean it is final; the next lookup tells.
	cancel(ctx context.Context, provider, clientOrderID string, submittedAt time.Time) error
}

// gridPendingOrder is an order whose outcome is not final yet.
type gridPendingOrder struct {
	clientOrderID string
	side          string
	level         int
	quantity      float64
	price         float64
	submittedAt   time.Time
	// provider is the exchange the order went to; lookups and cancels go
	// there even after a provider switch.
	provider string
	// unknown is true while the exchange has never confirmed the order.
	unknown bool
	// cancelRequestedAt is when the grid last asked to cancel the order.
	cancelRequestedAt time.Time
}

// nextClientOrderID returns a new client order ID for this run.
func (b *gridBook) nextClientOrderID() string {
	b.seq++
	return fmt.Sprintf("grid-%s-%d", b.runID, b.seq)
}

// gridStateFromStatus maps an exchange order status to a grid order state.
// Unrecognised statuses are treated as open so the order stays pending.
func gridStateFromStatus(status string) gridOrderState {
	switch status {
	case "FILLED":
		return gridOrderFilled
	case "CANCELED", "CANCELLED", "EXPIRED", "EXPIRED_IN_MATCH", "REJECTED":
		return gridOrderDone
	default:
		return gridOrderOpen
	}
}

// gridStep runs one tick. It reconciles a pending order first; only when no
// order is pending does it decide and (at most once) submit a new order. It
// returns the side that was filled this tick, or "".
func (s *BotServiceImpl) gridStep(ctx context.Context, grid gridParams, book *gridBook, currentPrice float64, ex gridExchange) string {
	if book.pending != nil {
		return s.reconcileGridOrder(ctx, grid, book, ex)
	}

	symbol := grid.symbol
	quantity := grid.quantity

	side, gridSize := gridAction(grid, currentPrice)
	if side == "" {
		// Waiting - price in middle of grid
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "WAITING",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Price: %.2f | Grid: %.2f | Range: %.2f-%.2f", currentPrice, gridSize, grid.lowerPrice, grid.upperPrice),
			Level:     "info",
		})
		return ""
	}

	level := gridLevel(grid, currentPrice)
	if ok, reason := book.check(side, level, quantity, currentPrice); !ok {
		logger.Info("Grid "+side+" skipped", "symbol", symbol, "price", currentPrice, "reason", reason)
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "SKIPPED",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Grid %s skipped at %.2f: %s", side, currentPrice, reason),
			Level:     "info",
		})
		return ""
	}

	if ctx.Err() != nil {
		return ""
	}

	clientOrderID := book.nextClientOrderID()
	logger.Info("Grid "+side+" signal", "symbol", symbol, "price", currentPrice, "qty", quantity, "level", level, "clientOrderId", clientOrderID)

	order := &gridPendingOrder{
		clientOrderID: clientOrderID,
		side:          side,
		level:         level,
		quantity:      quantity,
		price:         currentPrice,
		submittedAt:   book.now(),
	}
	if err := s.noteSubmission(ctx, symbol, order); err != nil {
		logger.Warn("Grid order not placed", "symbol", symbol, "side", side, "error", err)
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "ORDER_FAILED",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Grid %s %.4f @ %.2f not placed: %v", side, quantity, currentPrice, err),
			Level:     "error",
		})
		return ""
	}
	report, orderErr := ex.place(ctx, clientOrderID, side, quantity, currentPrice)
	order.provider = report.provider
	s.settleSubmission(symbol, order, orderErr != nil && exchange.IsOrderStateUnknown(orderErr))
	if orderErr != nil {
		if exchange.IsOrderStateUnknown(orderErr) {
			// The exchange may have accepted it: never re-send blindly.
			order.unknown = true
			book.pending = order
			logger.Warn("Grid order outcome unknown; reconciling before any retry", "symbol", symbol, "side", side, "clientOrderId", clientOrderID, "error", orderErr)
			s.broadcaster.BroadcastBotActivity(&model.BotActivity{
				Timestamp: time.Now(),
				Activity:  "ORDER_UNKNOWN",
				Symbol:    symbol,
				Message:   fmt.Sprintf("Grid %s %.4f @ %.2f outcome unknown (%s); checking the exchange before any retry: %v", side, quantity, currentPrice, clientOrderID, orderErr),
				Level:     "warning",
			})
			return ""
		}
		// A rejected order is not a trade: do not count it, do not change
		// the book, and never relabel it as a paper fill.
		logger.Warn("Grid order failed", "symbol", symbol, "side", side, "error", orderErr)
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "ORDER_FAILED",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Grid %s %.4f @ %.2f failed: %v", side, quantity, currentPrice, orderErr),
			Level:     "error",
		})
		return ""
	}

	return s.applyGridReport(grid, book, order, report)
}

// reconcileGridOrder looks the pending order up and applies the answer. It
// never submits an order.
func (s *BotServiceImpl) reconcileGridOrder(ctx context.Context, grid gridParams, book *gridBook, ex gridExchange) string {
	order := book.pending
	if ctx.Err() != nil {
		return ""
	}
	report, err := ex.lookup(ctx, order.provider, order.clientOrderID, order.submittedAt)
	if err != nil {
		level, msg := "warning", "lookup failed, will retry"
		if errors.Is(err, exchange.ErrReconcileUnsupported) {
			level, msg = "error", "this exchange cannot look orders up; the grid is paused until the order is reconciled manually and the bot restarted"
		}
		logger.Warn("Grid order reconciliation failed", "symbol", grid.symbol, "clientOrderId", order.clientOrderID, "error", err)
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "RECONCILING",
			Symbol:    grid.symbol,
			Message:   fmt.Sprintf("Grid %s %s pending: %s: %v", order.side, order.clientOrderID, msg, err),
			Level:     level,
		})
		return ""
	}
	wasUnknown := order.unknown
	filled := s.applyGridReport(grid, book, order, report)
	if wasUnknown && (book.pending != order || !order.unknown) {
		s.settleSubmission(grid.symbol, order, false)
	}
	if book.pending == order && report.state == gridOrderOpen {
		s.cancelStaleGridOrder(ctx, grid, book, order, ex)
	}
	return filled
}

// cancelStaleGridOrder requests the cancellation of an accepted order that
// has been open longer than gridOrderMaxAge. The order stays pending; the
// next lookup records any partial fill and frees the grid.
func (s *BotServiceImpl) cancelStaleGridOrder(ctx context.Context, grid gridParams, book *gridBook, order *gridPendingOrder, ex gridExchange) {
	now := book.now()
	if now.Sub(order.submittedAt) < gridOrderMaxAge {
		return
	}
	if !order.cancelRequestedAt.IsZero() && now.Sub(order.cancelRequestedAt) < gridCancelRetry {
		return
	}
	order.cancelRequestedAt = now
	err := ex.cancel(ctx, order.provider, order.clientOrderID, order.submittedAt)
	if err != nil {
		logger.Warn("Grid stale order cancel failed", "symbol", grid.symbol, "clientOrderId", order.clientOrderID, "error", err)
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "RECONCILING",
			Symbol:    grid.symbol,
			Message:   fmt.Sprintf("Grid %s %s open for %s; cancel failed, will retry: %v", order.side, order.clientOrderID, now.Sub(order.submittedAt).Round(time.Second), err),
			Level:     "warning",
		})
		return
	}
	logger.Info("Grid stale order cancel requested", "symbol", grid.symbol, "clientOrderId", order.clientOrderID)
	s.broadcaster.BroadcastBotActivity(&model.BotActivity{
		Timestamp: time.Now(),
		Activity:  "ORDER_CANCELING",
		Symbol:    grid.symbol,
		Message:   fmt.Sprintf("Grid %s %s open for %s; cancel requested", order.side, order.clientOrderID, now.Sub(order.submittedAt).Round(time.Second)),
		Level:     "info",
	})
}

// applyGridReport applies one order report to the book and clears or keeps
// the pending order.
func (s *BotServiceImpl) applyGridReport(grid gridParams, book *gridBook, order *gridPendingOrder, report gridOrderReport) string {
	switch report.state {
	case gridOrderFilled:
		book.pending = nil
		qty := report.executedQty
		if qty <= 0 {
			qty = order.quantity
		}
		s.recordGridFill(grid, book, order, qty)
		return order.side

	case gridOrderDone:
		book.pending = nil
		if report.executedQty > 0 {
			s.recordGridFill(grid, book, order, report.executedQty)
			return order.side
		}
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "ORDER_FAILED",
			Symbol:    grid.symbol,
			Message:   fmt.Sprintf("Grid %s %s ended without a fill", order.side, order.clientOrderID),
			Level:     "warning",
		})
		return ""

	case gridOrderNotFound:
		if book.now().Sub(order.submittedAt) < gridUnknownGrace {
			// The request may still be in flight; keep waiting.
			return ""
		}
		book.pending = nil
		logger.Warn("Grid order never reached the exchange", "symbol", grid.symbol, "clientOrderId", order.clientOrderID)
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "ORDER_FAILED",
			Symbol:    grid.symbol,
			Message:   fmt.Sprintf("Grid %s %s was not found on the exchange; the level can be retried", order.side, order.clientOrderID),
			Level:     "warning",
		})
		return ""

	default: // gridOrderOpen
		order.unknown = false
		book.pending = order
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "ORDER_OPEN",
			Symbol:    grid.symbol,
			Message:   fmt.Sprintf("Grid %s %s is open on the exchange (executed %.8f); waiting for it to fill", order.side, order.clientOrderID, report.executedQty),
			Level:     "info",
		})
		return ""
	}
}

// recordGridFill applies a confirmed fill to the book and the bot status.
func (s *BotServiceImpl) recordGridFill(grid gridParams, book *gridBook, order *gridPendingOrder, qty float64) {
	book.record(order.side, order.level, qty)
	logger.Info("Grid "+order.side+" executed", "symbol", grid.symbol, "price", order.price, "qty", qty, "position", book.position)

	s.broadcaster.BroadcastTradeNotification(&model.TradeNotification{
		ID:        fmt.Sprintf("trade_%d", time.Now().UnixMilli()),
		Symbol:    model.TradeSymbol(grid.symbol),
		Side:      model.OrderSide(order.side),
		Quantity:  qty,
		Price:     order.price,
		Total:     qty * order.price,
		Type:      "GRID_" + order.side,
		Timestamp: time.Now(),
		Message:   fmt.Sprintf("Grid %s %.4f @ %.2f (position %.8f)", order.side, qty, order.price, book.position),
	})

	s.runningMu.Lock()
	s.tradesCount++
	s.botStatus.TotalTrades = s.tradesCount
	s.botStatus.TotalProfit = s.totalProfit
	s.runningMu.Unlock()
}

// liveGridExchange adapts the configured exchange to gridExchange. record
// durably notes the (symbol, exchange) an order is about to go to; the kill
// switch drains by it, so no order is placed without it.
type liveGridExchange struct {
	symbol  string
	manager *exchange.ExchangeManager
	client  *bitkub.Client
	record  func(exchange string) error
}

// providerPlacer is the part of the exchange manager placement needs.
type providerPlacer interface {
	GetCurrentProvider() config.ExchangeProvider
	PlaceOrderWithClientIDOn(ctx context.Context, provider config.ExchangeProvider, symbol, side string, quantity, price float64, clientOrderID string) (*exchange.OrderReport, error)
	PlaceOrderOn(ctx context.Context, provider config.ExchangeProvider, symbol, side string, quantity, price float64) (interface{}, error)
}

// providerOrders is the part of the exchange manager lookups and cancels need.
type providerOrders interface {
	GetCurrentProvider() config.ExchangeProvider
	LookupOrderByClientIDOn(ctx context.Context, provider config.ExchangeProvider, symbol, clientOrderID string, since time.Time) (*exchange.OrderReport, error)
	CancelOrderByClientIDOn(ctx context.Context, provider config.ExchangeProvider, symbol, clientOrderID string, since time.Time) error
}

// orderProvider is the provider an order went to, or the current one for an
// order placed before providers were recorded.
func orderProvider(p providerOrders, provider string) config.ExchangeProvider {
	if provider == "" {
		return p.GetCurrentProvider()
	}
	return config.ExchangeProvider(provider)
}

func lookupOnProvider(ctx context.Context, p providerOrders, provider, symbol, clientOrderID string, submittedAt time.Time) (gridOrderReport, error) {
	report, err := p.LookupOrderByClientIDOn(ctx, orderProvider(p, provider), symbol, clientOrderID, submittedAt)
	if errors.Is(err, exchange.ErrOrderNotFound) {
		return gridOrderReport{state: gridOrderNotFound}, nil
	}
	if err != nil {
		return gridOrderReport{}, err
	}
	return gridOrderReport{state: gridStateFromStatus(report.Status), executedQty: report.ExecutedQty}, nil
}

func cancelOnProvider(ctx context.Context, p providerOrders, provider, symbol, clientOrderID string, submittedAt time.Time) error {
	err := p.CancelOrderByClientIDOn(ctx, orderProvider(p, provider), symbol, clientOrderID, submittedAt)
	if errors.Is(err, exchange.ErrOrderNotFound) {
		// Not open any more: the next lookup reports the final state.
		return nil
	}
	return err
}

func recordExchange(record func(string) error, ex string) error {
	if record == nil {
		return nil
	}
	if err := record(ex); err != nil {
		// %v, not %w: a timeout here must not read as an unknown exchange outcome.
		return fmt.Errorf("traded symbol not recorded, order not placed: %v", err)
	}
	return nil
}

// placeOnPinnedProvider reads the provider once, records it, and submits to
// that provider, so a provider switch in between cannot send the order to an
// exchange the kill switch does not know about.
func placeOnPinnedProvider(ctx context.Context, p providerPlacer, record func(string) error, symbol, clientOrderID, side string, quantity, price float64) (gridOrderReport, error) {
	provider := p.GetCurrentProvider()
	if err := recordExchange(record, string(provider)); err != nil {
		return gridOrderReport{}, err
	}
	pinned := gridOrderReport{provider: string(provider)}
	report, err := p.PlaceOrderWithClientIDOn(ctx, provider, symbol, side, quantity, price, clientOrderID)
	if err == nil {
		return gridOrderReport{state: gridStateFromStatus(report.Status), executedQty: report.ExecutedQty, provider: pinned.provider}, nil
	}
	if !errors.Is(err, exchange.ErrReconcileUnsupported) {
		return pinned, err
	}
	// No client order IDs on this exchange: a success is taken as a fill
	// (previous behavior) and an unknown outcome pauses the grid.
	if _, err := p.PlaceOrderOn(ctx, provider, symbol, side, quantity, price); err != nil {
		return pinned, err
	}
	return gridOrderReport{state: gridOrderFilled, executedQty: quantity, provider: pinned.provider}, nil
}

func (e *liveGridExchange) place(ctx context.Context, clientOrderID, side string, quantity, price float64) (gridOrderReport, error) {
	if e.manager != nil {
		return placeOnPinnedProvider(ctx, e.manager, e.record, e.symbol, clientOrderID, side, quantity, price)
	}
	if err := recordExchange(e.record, string(config.ExchangeBitkub)); err != nil {
		return gridOrderReport{}, err
	}
	if _, err := e.client.PlaceOrder(e.symbol, side, "MARKET", quantity, price); err != nil {
		return gridOrderReport{}, err
	}
	return gridOrderReport{state: gridOrderFilled, executedQty: quantity}, nil
}

func (e *liveGridExchange) lookup(ctx context.Context, provider, clientOrderID string, submittedAt time.Time) (gridOrderReport, error) {
	if e.manager == nil {
		return gridOrderReport{}, exchange.ErrReconcileUnsupported
	}
	return lookupOnProvider(ctx, e.manager, provider, e.symbol, clientOrderID, submittedAt)
}

func (e *liveGridExchange) cancel(ctx context.Context, provider, clientOrderID string, submittedAt time.Time) error {
	if e.manager == nil {
		return exchange.ErrReconcileUnsupported
	}
	return cancelOnProvider(ctx, e.manager, provider, e.symbol, clientOrderID, submittedAt)
}
