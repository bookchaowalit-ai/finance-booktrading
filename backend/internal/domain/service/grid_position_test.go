package service

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeGridExchange fills every accepted order immediately unless fail is set.
type fakeGridExchange struct {
	orders []string
	fail   error
}

func (f *fakeGridExchange) place(ctx context.Context, clientOrderID, side string, quantity, price float64) (gridOrderReport, error) {
	if f.fail != nil {
		return gridOrderReport{}, f.fail
	}
	f.orders = append(f.orders, side)
	return gridOrderReport{state: gridOrderFilled, executedQty: quantity}, nil
}

func (f *fakeGridExchange) cancel(ctx context.Context, provider, clientOrderID string, submittedAt time.Time) error {
	return nil
}

func (f *fakeGridExchange) lookup(ctx context.Context, provider, clientOrderID string, submittedAt time.Time) (gridOrderReport, error) {
	return gridOrderReport{state: gridOrderNotFound}, nil
}

func newGridTestService() *BotServiceImpl {
	return NewBotService(&MockBotStatusRepository{}, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
}

// gridSize = 25 -> BUY band <=125, SELL band >=175.
var testGrid = gridParams{symbol: "BTCUSDT", quantity: 1, gridLevels: 4, lowerPrice: 100, upperPrice: 200}

func TestGridBuyIsPlacedOncePerLevelWhilePriceStaysInBand(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	ex := &fakeGridExchange{}

	for tick := 0; tick < 50; tick++ {
		svc.gridStep(context.Background(), testGrid, book, 110, ex)
	}
	if len(ex.orders) != 1 {
		t.Fatalf("expected exactly 1 BUY while price sits in the buy band, got %d", len(ex.orders))
	}
	if book.position != 1 {
		t.Fatalf("position = %v, want 1", book.position)
	}
	if svc.tradesCount != 1 {
		t.Fatalf("tradesCount = %d, want 1", svc.tradesCount)
	}
}

func TestGridSellRequiresInventoryAndReleasesLevel(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	ex := &fakeGridExchange{}
	ctx := context.Background()

	// No naked sells: nothing bought yet.
	for tick := 0; tick < 10; tick++ {
		svc.gridStep(ctx, testGrid, book, 190, ex)
	}
	if len(ex.orders) != 0 {
		t.Fatalf("SELL without inventory must not be submitted, got %v", ex.orders)
	}

	svc.gridStep(ctx, testGrid, book, 110, ex) // BUY
	svc.gridStep(ctx, testGrid, book, 190, ex) // SELL
	svc.gridStep(ctx, testGrid, book, 190, ex) // no inventory left
	svc.gridStep(ctx, testGrid, book, 110, ex) // level released -> BUY again

	want := []string{"BUY", "SELL", "BUY"}
	if len(ex.orders) != len(want) {
		t.Fatalf("orders = %v, want %v", ex.orders, want)
	}
	for i := range want {
		if ex.orders[i] != want[i] {
			t.Fatalf("orders = %v, want %v", ex.orders, want)
		}
	}
	if book.position != 1 {
		t.Fatalf("position = %v, want 1", book.position)
	}
}

func TestGridBookPositionCap(t *testing.T) {
	grid := testGrid
	grid.maxPosition = 2
	book := newGridBook(grid)
	if ok, _ := book.check("BUY", 0, 1, 110); !ok {
		t.Fatal("first buy must be allowed")
	}
	book.record("BUY", 0, 1)
	book.record("BUY", 1, 1)
	if ok, reason := book.check("BUY", 2, 1, 110); ok {
		t.Fatal("buy above maxPosition must be rejected")
	} else if reason == "" {
		t.Fatal("rejection must carry a reason")
	}
}

func TestGridBookDefaultPositionCapIsOneFillPerLevel(t *testing.T) {
	book := newGridBook(testGrid)
	if book.maxPosition != 4 {
		t.Fatalf("default maxPosition = %v, want quantity*gridLevels = 4", book.maxPosition)
	}
	for l := 0; l < 4; l++ {
		book.record("BUY", l, 1)
	}
	if ok, _ := book.check("BUY", 99, 1, 110); ok {
		t.Fatal("buy beyond quantity*gridLevels must be rejected")
	}
}

func TestGridBookExposureCap(t *testing.T) {
	grid := testGrid
	grid.investment = 150 // quote currency
	book := newGridBook(grid)
	if ok, _ := book.check("BUY", 0, 1, 110); !ok {
		t.Fatal("110 of exposure is within a 150 cap")
	}
	book.record("BUY", 0, 1)
	if ok, _ := book.check("BUY", 1, 1, 110); ok {
		t.Fatal("220 of exposure must exceed a 150 cap")
	}
}

func TestGridFailedOrderIsNotCountedOrRelabelled(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	ex := &fakeGridExchange{fail: errors.New("exchange rejected")}

	if got := svc.gridStep(context.Background(), testGrid, book, 110, ex); got != "" {
		t.Fatalf("failed order must not report a fill, got %q", got)
	}
	if svc.tradesCount != 0 {
		t.Fatalf("failed order must not be counted, tradesCount = %d", svc.tradesCount)
	}
	if book.position != 0 || book.heldLevels[0] {
		t.Fatal("failed order must not change the book")
	}

	ex.fail = nil
	if got := svc.gridStep(context.Background(), testGrid, book, 110, ex); got != "BUY" {
		t.Fatalf("level must stay open after a failed order, got %q", got)
	}
}

func TestGridStepDoesNothingAfterCancel(t *testing.T) {
	svc := newGridTestService()
	book := newGridBook(testGrid)
	ex := &fakeGridExchange{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	svc.gridStep(ctx, testGrid, book, 110, ex)
	if len(ex.orders) != 0 {
		t.Fatal("no order may be placed after the run context is cancelled")
	}
}

func TestGridLevel(t *testing.T) {
	cases := map[float64]int{50: 0, 100: 0, 124.9: 0, 125: 1, 199: 3, 200: 3, 500: 3}
	for price, want := range cases {
		if got := gridLevel(testGrid, price); got != want {
			t.Fatalf("gridLevel(%v) = %d, want %d", price, got, want)
		}
	}
}
