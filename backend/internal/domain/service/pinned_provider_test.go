package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"trading-bot-system/backend/internal/adapter/exchange"
	"trading-bot-system/backend/internal/config"
)

// switchingPlacer switches its provider while the traded symbol is being
// recorded, the way a concurrent provider change can.
type switchingPlacer struct {
	provider config.ExchangeProvider
	placedOn []config.ExchangeProvider
}

func (p *switchingPlacer) GetCurrentProvider() config.ExchangeProvider { return p.provider }
func (p *switchingPlacer) PlaceOrderWithClientIDOn(ctx context.Context, provider config.ExchangeProvider, symbol, side string, quantity, price float64, clientOrderID string) (*exchange.OrderReport, error) {
	p.placedOn = append(p.placedOn, provider)
	return &exchange.OrderReport{Status: "NEW"}, nil
}
func (p *switchingPlacer) PlaceOrderOn(ctx context.Context, provider config.ExchangeProvider, symbol, side string, quantity, price float64) (interface{}, error) {
	p.placedOn = append(p.placedOn, provider)
	return nil, nil
}

// The exchange recorded for the kill switch is the one the order goes to.
func TestOrderGoesToTheRecordedProvider(t *testing.T) {
	p := &switchingPlacer{provider: config.ExchangeBinance}
	var recorded []string
	record := func(ex string) error {
		recorded = append(recorded, ex)
		p.provider = config.ExchangeBinanceTH // a switch right after the read
		return nil
	}
	if _, err := placeOnPinnedProvider(context.Background(), p, record, "", "BTCUSDT", "grid-x-1", "BUY", 1, 100); err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || recorded[0] != "binance" {
		t.Fatalf("recorded %v", recorded)
	}
	if len(p.placedOn) != 1 || p.placedOn[0] != config.ExchangeBinance {
		t.Fatalf("placed on %v, want binance", p.placedOn)
	}
}

// No durable record, no order.
func TestOrderRefusedWhenExchangeNotRecorded(t *testing.T) {
	p := &switchingPlacer{provider: config.ExchangeBinanceTH}
	record := func(string) error { return errors.New("db down") }
	if _, err := placeOnPinnedProvider(context.Background(), p, record, "", "BTCTHB", "grid-x-2", "BUY", 1, 100); err == nil {
		t.Fatal("want an error")
	} else if exchange.IsOrderStateUnknown(err) {
		t.Fatal("a refused submission is not an unknown outcome")
	}
	if len(p.placedOn) != 0 {
		t.Fatalf("order placed without a record: %v", p.placedOn)
	}
}

// A timed-out symbol write is "not submitted", never an unknown exchange
// outcome: that would park a pending order that does not exist.
func TestRecordTimeoutIsNotAnUnknownOutcome(t *testing.T) {
	p := &switchingPlacer{provider: config.ExchangeBinanceTH}
	record := func(string) error { return context.DeadlineExceeded }
	_, err := placeOnPinnedProvider(context.Background(), p, record, "", "BTCTHB", "grid-x-3", "BUY", 1, 100)
	if err == nil || exchange.IsOrderStateUnknown(err) {
		t.Fatalf("got %v, want a definite not-submitted error", err)
	}
}

// The placement report names the provider, also with an unknown outcome, so
// the order can be followed up where it went.
func TestPlacementReportNamesTheProvider(t *testing.T) {
	p := &switchingPlacer{provider: config.ExchangeBinance}
	report, err := placeOnPinnedProvider(context.Background(), p, nil, "", "BTCUSDT", "grid-x-4", "BUY", 1, 100)
	if err != nil || report.provider != "binance" {
		t.Fatalf("got %+v, %v", report, err)
	}
	u := &unknownPlacer{switchingPlacer{provider: config.ExchangeBinanceTH}}
	report, err = placeOnPinnedProvider(context.Background(), u, nil, "", "BTCTHB", "grid-x-5", "BUY", 1, 100)
	if !exchange.IsOrderStateUnknown(err) || report.provider != "binance_th" {
		t.Fatalf("got %+v, %v", report, err)
	}
}

type unknownPlacer struct{ switchingPlacer }

func (p *unknownPlacer) PlaceOrderWithClientIDOn(ctx context.Context, provider config.ExchangeProvider, symbol, side string, quantity, price float64, clientOrderID string) (*exchange.OrderReport, error) {
	return nil, exchange.ErrOrderStateUnknown
}

// recordingOrders answers lookups and cancels and notes the provider named.
type recordingOrders struct {
	current config.ExchangeProvider
	on      []config.ExchangeProvider
}

func (r *recordingOrders) GetCurrentProvider() config.ExchangeProvider { return r.current }
func (r *recordingOrders) LookupOrderByClientIDOn(ctx context.Context, provider config.ExchangeProvider, symbol, clientOrderID string, since time.Time) (*exchange.OrderReport, error) {
	r.on = append(r.on, provider)
	return &exchange.OrderReport{Status: "NEW"}, nil
}
func (r *recordingOrders) CancelOrderByClientIDOn(ctx context.Context, provider config.ExchangeProvider, symbol, clientOrderID string, since time.Time) error {
	r.on = append(r.on, provider)
	return nil
}

// After a provider switch, an order is looked up and cancelled where it was
// placed; an order with no recorded provider uses the current one.
func TestLookupAndCancelUseTheOrdersProvider(t *testing.T) {
	r := &recordingOrders{current: config.ExchangeBinanceTH}
	ctx := context.Background()
	if _, err := lookupOnProvider(ctx, r, "binance", "BTCUSDT", "grid-x-6", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := cancelOnProvider(ctx, r, "binance", "BTCUSDT", "grid-x-6", time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := lookupOnProvider(ctx, r, "", "BTCTHB", "grid-x-7", time.Now()); err != nil {
		t.Fatal(err)
	}
	want := []config.ExchangeProvider{config.ExchangeBinance, config.ExchangeBinance, config.ExchangeBinanceTH}
	if fmt.Sprint(r.on) != fmt.Sprint(want) {
		t.Fatalf("providers %v, want %v", r.on, want)
	}
}

// The grid follows a pending order up on the provider it was placed on.
func TestGridPendingOrderKeepsItsProvider(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	now := fixedClock(book)
	ex := newScriptedExchange()
	ex.placeResp = gridOrderReport{state: gridOrderOpen, provider: "binance"}
	ctx := context.Background()

	svc.gridStep(ctx, testGrid, book, 110, ex)
	if book.pending == nil || book.pending.provider != "binance" {
		t.Fatalf("pending %+v", book.pending)
	}
	ex.status[ex.placed[0]] = gridOrderReport{state: gridOrderOpen}
	*now = now.Add(gridOrderMaxAge + time.Second)
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if len(ex.lookupOn) != 1 || ex.lookupOn[0] != "binance" || len(ex.cancelOn) != 1 || ex.cancelOn[0] != "binance" {
		t.Fatalf("lookups on %v, cancels on %v", ex.lookupOn, ex.cancelOn)
	}
}

// An unknown submission keeps its provider when Stop reconciles it.
func TestParkedOrderIsReconciledOnItsProvider(t *testing.T) {
	stub := &lookupStub{report: gridOrderReport{state: gridOrderDone}}
	s := NewBotService(&MockBotStatusRepository{}, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	s.orderLookup = func(string) gridExchange { return stub }
	o := unknownOrder("grid-x-8", time.Hour)
	o.provider = "binance"
	s.parkUnknownOrders("BTCUSDT", o)
	s.reconcileUnresolved()
	if len(stub.on) != 1 || stub.on[0] != "binance" {
		t.Fatalf("looked up on %v", stub.on)
	}
}

// A pinned provider is used as given, never re-read.
func TestPlacementUsesThePinnedProvider(t *testing.T) {
	p := &switchingPlacer{provider: config.ExchangeBinanceTH}
	var recorded []string
	record := func(ex string) error { recorded = append(recorded, ex); return nil }
	if _, err := placeOnPinnedProvider(context.Background(), p, record, "binance", "BTCUSDT", "grid-x-9", "BUY", 1, 100); err != nil {
		t.Fatal(err)
	}
	if len(recorded) != 1 || recorded[0] != "binance" || len(p.placedOn) != 1 || p.placedOn[0] != config.ExchangeBinance {
		t.Fatalf("recorded %v, placed on %v", recorded, p.placedOn)
	}
}
