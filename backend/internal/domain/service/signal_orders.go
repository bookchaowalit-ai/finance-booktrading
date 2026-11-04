package service

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"trading-bot-system/backend/internal/adapter/exchange"
	"trading-bot-system/backend/internal/domain/model"
	"trading-bot-system/backend/internal/logger"
)

// Signal / auto order reconciliation.
//
// Signal and auto orders reuse the grid's rules (see grid_orders.go): every
// order carries a client order ID, an order whose outcome is unknown or that
// is accepted but not filled becomes the symbol's pending order, and while a
// symbol has a pending order no other order is placed for it. Each later
// signal or stop-loss/take-profit check for that symbol looks the order up
// first. This matters most for exits: a timed-out SELL is never re-sent
// blindly, so a position cannot be sold twice.

// signalOrderBook holds the pending signal/auto orders of one run. It is
// owned by the run's loop goroutine and needs no locking.
type signalOrderBook struct {
	pending map[string]*gridPendingOrder // symbol -> pending order
	runID   string
	seq     int
	now     func() time.Time
}

func newSignalOrderBook() *signalOrderBook {
	return &signalOrderBook{
		pending: make(map[string]*gridPendingOrder),
		runID:   strconv.FormatInt(time.Now().UnixNano(), 36),
		now:     time.Now,
	}
}

func (b *signalOrderBook) nextClientOrderID() string {
	b.seq++
	return fmt.Sprintf("sig-%s-%d", b.runID, b.seq)
}

// signalTradeStep submits one signal-driven order and records it. A failed
// order is not a trade: it is reported as ORDER_FAILED, is not counted in
// tradesCount/TotalTrades, is never relabelled as a "PAPER" fill, and does not
// open an auto-mode position. An order with an unknown outcome, or one that
// is open but not filled, is kept pending and reconciled on the next call for
// the same symbol. It reports whether an order of this side filled.
func (s *BotServiceImpl) signalTradeStep(ctx context.Context, book *signalOrderBook, symbol, side string, quantity, currentPrice float64, ex gridExchange) bool {
	if ctx.Err() != nil {
		return false
	}

	if pending := book.pending[symbol]; pending != nil {
		filledSide, final := s.reconcileSignalOrder(ctx, book, symbol, pending, ex)
		if !final {
			return false
		}
		if filledSide == side {
			// The earlier order already did what this call asks for.
			return true
		}
		if ctx.Err() != nil {
			return false
		}
	}

	// Never sell more than the tracked auto position (a partial exit may
	// already have reduced it).
	if side == "SELL" {
		s.runningMu.RLock()
		if pos := s.positions[symbol]; s.botMode == model.BotModeAuto && pos != nil && pos.quantity > 0 && pos.quantity < quantity {
			quantity = pos.quantity
		}
		s.runningMu.RUnlock()
	}

	order := &gridPendingOrder{
		clientOrderID: book.nextClientOrderID(),
		side:          side,
		quantity:      quantity,
		price:         currentPrice,
		submittedAt:   book.now(),
		provider:      ex.currentProvider(),
	}
	if err := s.noteSubmission(ctx, symbol, order); err != nil {
		logger.Warn("Signal trade order not placed", "symbol", symbol, "side", side, "error", err)
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "ORDER_FAILED",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Signal %s %.4f @ %.2f not placed: %v", side, quantity, currentPrice, err),
			Level:     "error",
		})
		return false
	}
	report, orderErr := ex.place(ctx, order.provider, order.clientOrderID, side, quantity, currentPrice)
	order.provider = report.provider
	s.settleSubmission(symbol, order, orderErr != nil && exchange.IsOrderStateUnknown(orderErr))
	if orderErr != nil {
		if exchange.IsOrderStateUnknown(orderErr) {
			order.unknown = true
			book.pending[symbol] = order
			logger.Warn("Signal order outcome unknown; reconciling before any retry", "symbol", symbol, "side", side, "clientOrderId", order.clientOrderID, "error", orderErr)
			s.broadcaster.BroadcastBotActivity(&model.BotActivity{
				Timestamp: time.Now(),
				Activity:  "ORDER_UNKNOWN",
				Symbol:    symbol,
				Message:   fmt.Sprintf("Signal %s %.4f @ %.2f outcome unknown (%s); checking the exchange before any retry: %v", side, quantity, currentPrice, order.clientOrderID, orderErr),
				Level:     "warning",
			})
			return false
		}
		logger.Warn("Signal trade order failed", "symbol", symbol, "side", side, "error", orderErr)
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "ORDER_FAILED",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Signal %s %.4f @ %.2f failed: %v", side, quantity, currentPrice, orderErr),
			Level:     "error",
		})
		return false
	}
	filledSide, _ := s.applySignalReport(ctx, book, symbol, order, report)
	return filledSide == side
}

// reconcileSignalOrder looks a pending order up and applies the answer. It
// never submits an order. final is false while the order is still pending.
func (s *BotServiceImpl) reconcileSignalOrder(ctx context.Context, book *signalOrderBook, symbol string, order *gridPendingOrder, ex gridExchange) (filledSide string, final bool) {
	report, err := ex.lookup(ctx, order.provider, order.clientOrderID, order.submittedAt)
	if err != nil {
		logger.Warn("Signal order reconciliation failed", "symbol", symbol, "clientOrderId", order.clientOrderID, "error", err)
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "RECONCILING",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Signal %s %s pending; no new order for %s until it is reconciled: %v", order.side, order.clientOrderID, symbol, err),
			Level:     "warning",
		})
		return "", false
	}
	wasUnknown := order.unknown
	filledSide, final = s.applySignalReport(ctx, book, symbol, order, report)
	if wasUnknown && (final || !order.unknown) {
		s.settleSubmission(symbol, order, false)
	}
	if !final && report.state == gridOrderOpen {
		s.cancelStaleSignalOrder(ctx, book, symbol, order, ex)
	}
	return filledSide, final
}

// applySignalReport applies one order report. It returns the side that
// fully filled ("" otherwise) and whether the order is final.
func (s *BotServiceImpl) applySignalReport(ctx context.Context, book *signalOrderBook, symbol string, order *gridPendingOrder, report gridOrderReport) (string, bool) {
	switch report.state {
	case gridOrderFilled:
		delete(book.pending, symbol)
		qty := report.executedQty
		if qty <= 0 {
			qty = order.quantity
		}
		s.recordSignalFill(ctx, symbol, order, qty)
		return order.side, true

	case gridOrderDone:
		delete(book.pending, symbol)
		if report.executedQty > 0 {
			s.recordSignalFill(ctx, symbol, order, report.executedQty)
			if report.executedQty+positionEpsilon >= order.quantity {
				return order.side, true
			}
			return "", true
		}
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "ORDER_FAILED",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Signal %s %s ended without a fill", order.side, order.clientOrderID),
			Level:     "warning",
		})
		return "", true

	case gridOrderNotFound:
		if book.now().Sub(order.submittedAt) < gridUnknownGrace {
			return "", false
		}
		delete(book.pending, symbol)
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "ORDER_FAILED",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Signal %s %s was not found on the exchange", order.side, order.clientOrderID),
			Level:     "warning",
		})
		return "", true

	default: // gridOrderOpen
		order.unknown = false
		book.pending[symbol] = order
		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "ORDER_OPEN",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Signal %s %s is open on the exchange (executed %.8f); waiting for it to fill", order.side, order.clientOrderID, report.executedQty),
			Level:     "info",
		})
		return "", false
	}
}

// cancelStaleSignalOrder cancels an accepted signal order that has been open
// longer than gridOrderMaxAge. It stays pending until a lookup reports it
// final.
func (s *BotServiceImpl) cancelStaleSignalOrder(ctx context.Context, book *signalOrderBook, symbol string, order *gridPendingOrder, ex gridExchange) {
	now := book.now()
	if now.Sub(order.submittedAt) < gridOrderMaxAge {
		return
	}
	if !order.cancelRequestedAt.IsZero() && now.Sub(order.cancelRequestedAt) < gridCancelRetry {
		return
	}
	order.cancelRequestedAt = now
	activity, level, msg := "ORDER_CANCELING", "info", "cancel requested"
	if err := ex.cancel(ctx, order.provider, order.clientOrderID, order.submittedAt); err != nil {
		activity, level, msg = "RECONCILING", "warning", fmt.Sprintf("cancel failed, will retry: %v", err)
	}
	s.broadcaster.BroadcastBotActivity(&model.BotActivity{
		Timestamp: time.Now(),
		Activity:  activity,
		Symbol:    symbol,
		Message:   fmt.Sprintf("Signal %s %s open for %s; %s", order.side, order.clientOrderID, now.Sub(order.submittedAt).Round(time.Second), msg),
		Level:     level,
	})
}

// recordSignalFill counts a confirmed fill and updates the auto position.
func (s *BotServiceImpl) recordSignalFill(ctx context.Context, symbol string, order *gridPendingOrder, qty float64) {
	side := order.side
	tradeType := fmt.Sprintf("SIGNAL_%s", side)
	s.broadcaster.BroadcastTradeNotification(&model.TradeNotification{
		ID:        fmt.Sprintf("signal_trade_%d", time.Now().UnixMilli()),
		Symbol:    model.TradeSymbol(symbol),
		Side:      model.OrderSide(side),
		Quantity:  qty,
		Price:     order.price,
		Total:     qty * order.price,
		Type:      tradeType,
		Timestamp: time.Now(),
		Message:   fmt.Sprintf("[%s] %s %.4f @ %.2f", strings.ToUpper(tradeType), side, qty, order.price),
	})

	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	if s.botMode == model.BotModeAuto && ctx.Err() == nil {
		pos := s.positions[symbol]
		switch {
		case side == "BUY" && pos == nil:
			s.positions[symbol] = &positionInfo{entryPrice: order.price, quantity: qty, entryTime: time.Now()}
		case side == "BUY":
			pos.quantity += qty
		case side == "SELL" && pos != nil:
			pos.quantity -= qty
			if pos.quantity <= positionEpsilon {
				delete(s.positions, symbol)
			}
		}
	}
	s.tradesCount++
	if s.botMode == model.BotModeSignal || s.botMode == model.BotModeAuto {
		s.botStatus.TotalTrades = s.tradesCount
	}
}
