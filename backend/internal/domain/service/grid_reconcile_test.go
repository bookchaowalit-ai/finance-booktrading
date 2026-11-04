package service

import (
	"context"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"trading-bot-system/backend/internal/adapter/exchange"
)

// scriptedExchange records every submission and answers lookups from a map
// keyed by client order ID. placeErr, when set, is returned by the next place
// call only (the order is still recorded, as a timed-out request would be).
type scriptedExchange struct {
	placed    []string // client order IDs, in order
	sides     []string
	placeErr  error
	placeResp gridOrderReport
	status    map[string]gridOrderReport
	lookupErr error
	lookups   int
	cancels   []string
	cancelErr error
	// lookupOn and cancelOn are the providers lookups and cancels named.
	lookupOn []string
	cancelOn []string
}

func newScriptedExchange() *scriptedExchange {
	return &scriptedExchange{
		placeResp: gridOrderReport{state: gridOrderFilled, executedQty: 1},
		status:    map[string]gridOrderReport{},
	}
}

// currentProvider is placeResp's provider: the one place reports.
func (e *scriptedExchange) currentProvider() string { return e.placeResp.provider }

func (e *scriptedExchange) place(ctx context.Context, provider, clientOrderID, side string, quantity, price float64) (gridOrderReport, error) {
	e.placed = append(e.placed, clientOrderID)
	e.sides = append(e.sides, side)
	if err := e.placeErr; err != nil {
		e.placeErr = nil
		return gridOrderReport{provider: e.placeResp.provider}, err
	}
	return e.placeResp, nil
}

func (e *scriptedExchange) lookup(ctx context.Context, provider, clientOrderID string, submittedAt time.Time) (gridOrderReport, error) {
	e.lookups++
	e.lookupOn = append(e.lookupOn, provider)
	if e.lookupErr != nil {
		return gridOrderReport{}, e.lookupErr
	}
	if r, ok := e.status[clientOrderID]; ok {
		return r, nil
	}
	return gridOrderReport{state: gridOrderNotFound}, nil
}

func (e *scriptedExchange) cancel(ctx context.Context, provider, clientOrderID string, submittedAt time.Time) error {
	e.cancels = append(e.cancels, clientOrderID)
	e.cancelOn = append(e.cancelOn, provider)
	return e.cancelErr
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func unknownErrors() map[string]error {
	return map[string]error{
		"sentinel": fmt.Errorf("binance: %w", exchange.ErrOrderStateUnknown),
		"deadline": fmt.Errorf("post: %w", context.DeadlineExceeded),
		"net":      timeoutErr{},
	}
}

func fixedClock(book *gridBook) *time.Time {
	now := time.Unix(1_700_000_000, 0)
	book.now = func() time.Time { return now }
	return &now
}

func TestGridUnknownOrderThatFilledIsNeverResubmitted(t *testing.T) {
	for name, uerr := range unknownErrors() {
		t.Run(name, func(t *testing.T) {
			svc := newGridTestService()
			book := newGridBook(testGrid)
			fixedClock(book)
			ex := newScriptedExchange()
			ctx := context.Background()

			ex.placeErr = uerr
			if got := svc.gridStep(ctx, testGrid, book, 110, ex); got != "" {
				t.Fatalf("unknown outcome must not report a fill, got %q", got)
			}
			if book.pending == nil || !book.pending.unknown {
				t.Fatal("unknown outcome must leave a pending order")
			}
			if svc.tradesCount != 0 || book.position != 0 {
				t.Fatal("unknown outcome must not be counted or change the book")
			}

			// Price stays in the buy band: the grid must only look the order up.
			for i := 0; i < 5; i++ {
				svc.gridStep(ctx, testGrid, book, 110, ex)
			}
			if len(ex.placed) != 1 {
				t.Fatalf("order re-submitted while its outcome was unknown: %v", ex.placed)
			}

			// The exchange did accept it and it filled.
			ex.status[ex.placed[0]] = gridOrderReport{state: gridOrderFilled, executedQty: 1}
			if got := svc.gridStep(ctx, testGrid, book, 110, ex); got != "BUY" {
				t.Fatalf("reconciled fill must be reported, got %q", got)
			}
			if book.pending != nil || book.position != 1 || svc.tradesCount != 1 {
				t.Fatalf("fill not applied: pending=%v position=%v trades=%d", book.pending, book.position, svc.tradesCount)
			}
			// The level is now held: still no second BUY.
			svc.gridStep(ctx, testGrid, book, 110, ex)
			if len(ex.placed) != 1 {
				t.Fatalf("level re-bought after reconciled fill: %v", ex.placed)
			}
		})
	}
}

func TestGridUnknownOrderNotFoundIsRetriedOnlyAfterGrace(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	now := fixedClock(book)
	ex := newScriptedExchange()
	ctx := context.Background()

	ex.placeErr = timeoutErr{}
	svc.gridStep(ctx, testGrid, book, 110, ex)

	// Not found, but still inside the grace window: the request may be in flight.
	*now = now.Add(gridUnknownGrace - time.Second)
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if book.pending == nil || len(ex.placed) != 1 {
		t.Fatalf("not-found inside grace must keep waiting: pending=%v placed=%v", book.pending, ex.placed)
	}

	// Past the grace window the order is treated as never placed...
	*now = now.Add(2 * time.Second)
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if book.pending != nil {
		t.Fatal("not-found after grace must clear the pending order")
	}
	if len(ex.placed) != 1 || svc.tradesCount != 0 || book.position != 0 {
		t.Fatal("clearing a never-placed order must not count it or submit in the same tick")
	}

	// ...and the next tick may retry with a fresh client order ID.
	if got := svc.gridStep(ctx, testGrid, book, 110, ex); got != "BUY" {
		t.Fatalf("level must be retryable after reconciliation, got %q", got)
	}
	if len(ex.placed) != 2 || ex.placed[0] == ex.placed[1] {
		t.Fatalf("retry must use a new client order ID: %v", ex.placed)
	}
}

func TestGridLookupFailureKeepsOrderPending(t *testing.T) {
	for name, lerr := range map[string]error{
		"transient":   errors.New("lookup timed out"),
		"unsupported": fmt.Errorf("%w: bitkub", exchange.ErrReconcileUnsupported),
	} {
		t.Run(name, func(t *testing.T) {
			svc := newGridTestService()
			book := newGridBook(testGrid)
			now := fixedClock(book)
			ex := newScriptedExchange()
			ctx := context.Background()

			ex.placeErr = timeoutErr{}
			svc.gridStep(ctx, testGrid, book, 110, ex)
			ex.lookupErr = lerr
			*now = now.Add(10 * gridUnknownGrace)
			for i := 0; i < 5; i++ {
				svc.gridStep(ctx, testGrid, book, 190, ex)
				svc.gridStep(ctx, testGrid, book, 110, ex)
			}
			if len(ex.placed) != 1 || book.pending == nil {
				t.Fatalf("a failed lookup must never lead to a new order: placed=%v pending=%v", ex.placed, book.pending)
			}
		})
	}
}

func TestGridOpenOrderWaitsForFill(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	fixedClock(book)
	ex := newScriptedExchange()
	ctx := context.Background()

	ex.placeResp = gridOrderReport{state: gridOrderOpen}
	if got := svc.gridStep(ctx, testGrid, book, 110, ex); got != "" {
		t.Fatalf("an accepted but unfilled order is not a fill, got %q", got)
	}
	if book.pending == nil || book.pending.unknown {
		t.Fatal("an open order must be pending and known")
	}
	id := ex.placed[0]

	ex.status[id] = gridOrderReport{state: gridOrderOpen, executedQty: 0.4}
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if len(ex.placed) != 1 || book.position != 0 {
		t.Fatal("a partially filled open order must keep waiting")
	}

	// Cancelled after a partial fill: the partial quantity is inventory.
	ex.status[id] = gridOrderReport{state: gridOrderDone, executedQty: 0.4}
	if got := svc.gridStep(ctx, testGrid, book, 110, ex); got != "BUY" {
		t.Fatalf("partial fill must be recorded, got %q", got)
	}
	if book.pending != nil || book.position != 0.4 || !book.heldLevels[0] {
		t.Fatalf("partial fill not applied: pending=%v position=%v", book.pending, book.position)
	}
}

func TestGridDoneWithoutFillFreesLevel(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	fixedClock(book)
	ex := newScriptedExchange()
	ctx := context.Background()

	ex.placeResp = gridOrderReport{state: gridOrderOpen}
	svc.gridStep(ctx, testGrid, book, 110, ex)
	ex.status[ex.placed[0]] = gridOrderReport{state: gridOrderDone}
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if book.pending != nil || book.position != 0 || svc.tradesCount != 0 {
		t.Fatal("an order that ended unfilled must clear without a trade")
	}
	ex.placeResp = gridOrderReport{state: gridOrderFilled, executedQty: 1}
	if got := svc.gridStep(ctx, testGrid, book, 110, ex); got != "BUY" {
		t.Fatalf("level must be free again, got %q", got)
	}
}

func TestGridPartialSellDoesNotReleaseLevel(t *testing.T) {
	book := newGridBook(testGrid)
	book.record("BUY", 0, 1)
	book.record("SELL", 0, 0.5)
	if !book.heldLevels[0] {
		t.Fatal("a partial SELL must not free the level")
	}
	book.record("SELL", 0, 1)
	if book.heldLevels[0] {
		t.Fatal("a full SELL must free the level")
	}
}

func TestGridClientOrderIDsAreUniqueAndValid(t *testing.T) {
	book := newGridBook(testGrid)
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		id := book.nextClientOrderID()
		if seen[id] {
			t.Fatalf("duplicate client order ID %q", id)
		}
		if !exchange.ValidClientOrderID(id) {
			t.Fatalf("client order ID %q is not accepted by the exchange", id)
		}
		seen[id] = true
	}
}

func TestGridStateFromStatus(t *testing.T) {
	cases := map[string]gridOrderState{
		"FILLED": gridOrderFilled, "NEW": gridOrderOpen, "PARTIALLY_FILLED": gridOrderOpen,
		"CANCELED": gridOrderDone, "EXPIRED": gridOrderDone, "REJECTED": gridOrderDone,
		"SOMETHING_NEW": gridOrderOpen,
	}
	for status, want := range cases {
		if got := gridStateFromStatus(status); got != want {
			t.Fatalf("gridStateFromStatus(%q) = %v, want %v", status, got, want)
		}
	}
}

func TestGridStaleOpenOrderIsCancelledThenReconciled(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	now := fixedClock(book)
	ex := newScriptedExchange()
	ctx := context.Background()

	ex.placeResp = gridOrderReport{state: gridOrderOpen}
	svc.gridStep(ctx, testGrid, book, 110, ex)
	id := ex.placed[0]
	ex.status[id] = gridOrderReport{state: gridOrderOpen}

	*now = now.Add(gridOrderMaxAge - time.Second)
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if len(ex.cancels) != 0 {
		t.Fatalf("cancelled before max age: %v", ex.cancels)
	}

	*now = now.Add(2 * time.Second)
	svc.gridStep(ctx, testGrid, book, 110, ex)
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if len(ex.cancels) != 1 || ex.cancels[0] != id {
		t.Fatalf("want exactly one cancel of %s, got %v", id, ex.cancels)
	}
	if book.pending == nil || len(ex.placed) != 1 {
		t.Fatal("a cancel request must keep the order pending and place nothing")
	}

	// The exchange cancels it after a partial fill: the fill is recorded.
	ex.status[id] = gridOrderReport{state: gridOrderDone, executedQty: 0.4}
	if got := svc.gridStep(ctx, testGrid, book, 110, ex); got != "BUY" {
		t.Fatalf("partial fill must be reported, got %q", got)
	}
	if book.pending != nil || book.position != 0.4 || svc.tradesCount != 1 {
		t.Fatalf("pending=%v position=%v trades=%d", book.pending, book.position, svc.tradesCount)
	}
}

func TestGridStaleOrderCancelFailureRetriesWithoutPlacing(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	now := fixedClock(book)
	ex := newScriptedExchange()
	ctx := context.Background()

	ex.placeResp = gridOrderReport{state: gridOrderOpen}
	svc.gridStep(ctx, testGrid, book, 110, ex)
	id := ex.placed[0]
	ex.status[id] = gridOrderReport{state: gridOrderOpen}
	ex.cancelErr = errors.New("cancel timed out")

	*now = now.Add(gridOrderMaxAge)
	svc.gridStep(ctx, testGrid, book, 110, ex)
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if len(ex.cancels) != 1 {
		t.Fatalf("cancel retried too soon: %v", ex.cancels)
	}
	*now = now.Add(gridCancelRetry)
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if len(ex.cancels) != 2 {
		t.Fatalf("cancel not retried after %s: %v", gridCancelRetry, ex.cancels)
	}
	if len(ex.placed) != 1 || book.pending == nil {
		t.Fatalf("a failed cancel must never lead to a new order: placed=%v", ex.placed)
	}

	// A fill that races the cancel is still recorded.
	ex.status[id] = gridOrderReport{state: gridOrderFilled, executedQty: 1}
	if got := svc.gridStep(ctx, testGrid, book, 110, ex); got != "BUY" || book.position != 1 {
		t.Fatalf("racing fill lost: got %q position %v", got, book.position)
	}
}

func TestGridUnconfirmedOrderIsNeverCancelled(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	now := fixedClock(book)
	ex := newScriptedExchange()
	ctx := context.Background()

	ex.placeErr = timeoutErr{}
	svc.gridStep(ctx, testGrid, book, 110, ex)
	ex.lookupErr = errors.New("lookup timed out")
	*now = now.Add(2 * gridOrderMaxAge)
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if len(ex.cancels) != 0 {
		t.Fatalf("an order the exchange never confirmed open must not be cancelled: %v", ex.cancels)
	}
}
