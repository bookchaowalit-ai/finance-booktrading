package service

import (
	"context"
	"errors"
	"sync"
	"testing"

	"trading-bot-system/backend/internal/domain/model"
)

// unresolvedStoreRepo persists unresolved submissions, shared between two
// service instances to simulate a backend restart.
type unresolvedStoreRepo struct {
	symbolStoreRepo
	umu     sync.Mutex
	orders  map[string]model.UnresolvedOrder
	saveErr error
	listErr error
	saved   []model.UnresolvedOrder // every save, in order
}

func (r *unresolvedStoreRepo) SaveUnresolvedOrder(ctx context.Context, o model.UnresolvedOrder) error {
	if r.saveErr != nil {
		return r.saveErr
	}
	r.umu.Lock()
	defer r.umu.Unlock()
	if r.orders == nil {
		r.orders = map[string]model.UnresolvedOrder{}
	}
	r.orders[o.ClientOrderID] = o
	r.saved = append(r.saved, o)
	return nil
}

func (r *unresolvedStoreRepo) DeleteUnresolvedOrder(ctx context.Context, id string) error {
	r.umu.Lock()
	defer r.umu.Unlock()
	delete(r.orders, id)
	return nil
}

func (r *unresolvedStoreRepo) ListUnresolvedOrders(ctx context.Context) ([]model.UnresolvedOrder, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	r.umu.Lock()
	defer r.umu.Unlock()
	out := []model.UnresolvedOrder{}
	for _, o := range r.orders {
		out = append(out, o)
	}
	return out, nil
}

// A submission with an unknown outcome is still reported, and reconciled on
// its exchange, after a backend restart.
func TestUnknownSubmissionSurvivesBackendRestart(t *testing.T) {
	repo := &unresolvedStoreRepo{}
	first := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	book := newGridBook(testGrid)
	fixedClock(book)
	ex := newScriptedExchange()
	ex.placeErr = unknownErrors()["sentinel"]
	ex.placeResp = gridOrderReport{provider: "binance"}
	first.gridStep(context.Background(), testGrid, book, 110, ex)
	if len(ex.placed) != 1 {
		t.Fatalf("placed %v", ex.placed)
	}
	// The record written before sending already names the exchange: a crash
	// mid-placement must not leave a record that means "current provider".
	if len(repo.saved) == 0 || repo.saved[0].Exchange != "binance" {
		t.Fatalf("first record %+v", repo.saved)
	}

	restarted := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	st, err := restarted.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.UnresolvedOrders) != 1 || st.UnresolvedOrders[0].ClientOrderID != ex.placed[0] || st.UnresolvedOrders[0].Exchange != "binance" {
		t.Fatalf("after restart got %+v", st.UnresolvedOrders)
	}

	stub := &lookupStub{report: gridOrderReport{state: gridOrderDone}}
	restarted.orderLookup = func(string) gridExchange { return stub }
	if err := restarted.Stop(context.Background()); err == nil {
		t.Fatal("want ErrBotNotRunning")
	}
	if len(stub.on) != 1 || stub.on[0] != "binance" {
		t.Fatalf("looked up on %v", stub.on)
	}
	st, _ = restarted.GetStatus(context.Background())
	if len(st.UnresolvedOrders) != 0 || len(repo.orders) != 0 {
		t.Fatalf("still unresolved: %+v %v", st.UnresolvedOrders, repo.orders)
	}
}

// A submission the exchange answered leaves no record behind.
func TestAnsweredSubmissionLeavesNoRecord(t *testing.T) {
	repo := &unresolvedStoreRepo{}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	book := newGridBook(testGrid)
	fixedClock(book)
	ex := newScriptedExchange()
	s.gridStep(context.Background(), testGrid, book, 110, ex)
	if len(ex.placed) != 1 || len(repo.orders) != 0 {
		t.Fatalf("placed %v, records %v", ex.placed, repo.orders)
	}
}

// An unknown submission the loop later finds on the exchange drops its record.
func TestReconciledSubmissionDropsItsRecord(t *testing.T) {
	repo := &unresolvedStoreRepo{}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	book := newGridBook(testGrid)
	fixedClock(book)
	ex := newScriptedExchange()
	ex.placeErr = unknownErrors()["sentinel"]
	s.gridStep(context.Background(), testGrid, book, 110, ex)
	if len(repo.orders) != 1 {
		t.Fatalf("records %v", repo.orders)
	}
	ex.status[ex.placed[0]] = gridOrderReport{state: gridOrderFilled, executedQty: 1}
	s.gridStep(context.Background(), testGrid, book, 110, ex)
	if len(repo.orders) != 0 {
		t.Fatalf("records %v", repo.orders)
	}
}

// No durable record, no order.
func TestSubmissionRefusedWhenNotRecorded(t *testing.T) {
	repo := &unresolvedStoreRepo{saveErr: errors.New("db down")}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	book := newGridBook(testGrid)
	fixedClock(book)
	ex := newScriptedExchange()
	s.gridStep(context.Background(), testGrid, book, 110, ex)
	if len(ex.placed) != 0 || book.pending != nil {
		t.Fatalf("placed %v without a record", ex.placed)
	}
}

// An unreadable store fails the status read rather than report none.
func TestStatusFailsWhenUnresolvedOrdersUnreadable(t *testing.T) {
	repo := &unresolvedStoreRepo{listErr: errors.New("db down")}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	if _, err := s.GetStatus(context.Background()); err == nil {
		t.Fatal("status must fail when unresolved orders cannot be read")
	}
}
