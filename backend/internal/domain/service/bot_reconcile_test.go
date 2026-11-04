package service

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"trading-bot-system/backend/internal/domain/model"
	"trading-bot-system/backend/internal/port/input"
)

// symbolStoreRepo is a bot status repo that also persists traded symbols,
// shared between two service instances to simulate a backend restart.
type symbolStoreRepo struct {
	MockBotStatusRepository
	mu      sync.Mutex
	symbols map[model.TradedSymbol]bool
	addErr  error
	listErr error
	onList  func() // runs while the list is being read
}

func (r *symbolStoreRepo) AddTradedSymbol(ctx context.Context, symbol, exchange string) error {
	if r.addErr != nil {
		return r.addErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.symbols == nil {
		r.symbols = map[model.TradedSymbol]bool{}
	}
	r.symbols[model.TradedSymbol{Symbol: symbol, Exchange: exchange}] = true
	return nil
}

func (r *symbolStoreRepo) ListTradedSymbols(ctx context.Context) ([]model.TradedSymbol, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	if r.onList != nil {
		r.onList()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []model.TradedSymbol{}
	for s := range r.symbols {
		out = append(out, s)
	}
	return out, nil
}

// A backend restart must not lose the symbols an earlier process traded:
// their LIMIT orders may still be open and the kill switch drains by symbol.
func TestTradedSymbolsSurviveBackendRestart(t *testing.T) {
	repo := &symbolStoreRepo{}
	first := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	if err := first.noteTradedSymbol(context.Background(), "XRPTHB", ""); err != nil {
		t.Fatal(err)
	}

	restarted := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	st, err := restarted.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.Symbols, ",") != "XRPTHB" {
		t.Fatalf("after restart got %v, want XRPTHB", st.Symbols)
	}
}

// If the symbol cannot be recorded durably, no order may be placed for it.
func TestStartRefusedWhenTradedSymbolCannotBePersisted(t *testing.T) {
	repo := &symbolStoreRepo{addErr: errors.New("db down")}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	if err := s.Start(context.Background(), validGrid()); err == nil {
		s.Stop(context.Background())
		t.Fatal("Start must fail when the traded symbol cannot be persisted")
	}
	if s.IsRunning(context.Background()) {
		t.Fatal("bot running without a durable symbol record")
	}
}

// An unreadable symbol store must fail the status read, not report fewer
// symbols (the kill switch would skip their orders).
func TestStatusFailsWhenTradedSymbolsUnreadable(t *testing.T) {
	repo := &symbolStoreRepo{listErr: errors.New("db down")}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	if _, err := s.GetStatus(context.Background()); err == nil {
		t.Fatal("status must fail when traded symbols cannot be read")
	}
}

// lookupStub answers order lookups for the Stop-time reconciliation.
type lookupStub struct {
	report gridOrderReport
	err    error
	calls  atomic.Int32
	on     []string
}

func (l *lookupStub) currentProvider() string { return "" }
func (l *lookupStub) place(context.Context, string, string, string, float64, float64) (gridOrderReport, error) {
	return gridOrderReport{}, errors.New("place must not be called")
}
func (l *lookupStub) lookup(ctx context.Context, provider, id string, at time.Time) (gridOrderReport, error) {
	l.calls.Add(1)
	l.on = append(l.on, provider)
	if ctx.Err() != nil {
		return gridOrderReport{}, ctx.Err()
	}
	return l.report, l.err
}
func (l *lookupStub) cancel(context.Context, string, string, time.Time) error { return nil }

func unknownOrder(id string, age time.Duration) *gridPendingOrder {
	return &gridPendingOrder{clientOrderID: id, side: "BUY", unknown: true, submittedAt: time.Now().Add(-age)}
}

// A grid loop stopped while its submission's outcome is unknown must leave
// the order for Stop to reconcile with a context of its own; status lists it
// until the exchange answers, so the kill switch cannot report containment.
func TestStopReconcilesUnknownGridOrder(t *testing.T) {
	cases := []struct {
		name    string
		stub    *lookupStub
		age     time.Duration
		pending bool
	}{
		{"exchange answers open", &lookupStub{report: gridOrderReport{state: gridOrderOpen}}, 0, false},
		{"exchange answers filled", &lookupStub{report: gridOrderReport{state: gridOrderFilled}}, 0, false},
		{"lookup fails", &lookupStub{err: errors.New("timeout")}, 0, true},
		{"not found, request may be in flight", &lookupStub{report: gridOrderReport{state: gridOrderNotFound}}, 0, true},
		{"not found after grace", &lookupStub{report: gridOrderReport{state: gridOrderNotFound}}, 2 * gridUnknownGrace, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := newRunningBot(t)
			s.orderLookup = func(string) gridExchange { return tc.stub }
			book := &gridBook{pending: unknownOrder("grid-x-1", tc.age)}
			s.goLoop(func() {
				defer s.parkUnknownOrders("BTCTHB", book.pending)
				<-s.ctx.Done()
			})
			if err := s.Stop(context.Background()); err != nil {
				t.Fatal(err)
			}
			if tc.stub.calls.Load() == 0 {
				t.Fatal("Stop did not look the unknown order up")
			}
			st, err := s.GetStatus(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := len(st.UnresolvedOrders) > 0; got != tc.pending {
				t.Fatalf("unresolved=%v, want %v", st.UnresolvedOrders, tc.pending)
			}
			if tc.pending && (st.UnresolvedOrders[0].Symbol != "BTCTHB" || st.UnresolvedOrders[0].ClientOrderID != "grid-x-1") {
				t.Fatalf("got %+v", st.UnresolvedOrders)
			}
		})
	}
}

// A kill retry gets 409 from Stop; it must still re-check what is unresolved.
func TestStopRetryReconcilesUnknownOrders(t *testing.T) {
	stub := &lookupStub{err: errors.New("timeout")}
	s := newRunningBot(t)
	s.orderLookup = func(string) gridExchange { return stub }
	s.parkUnknownOrders("ETHTHB", unknownOrder("sig-x-1", 0))
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetStatus(context.Background())
	if len(st.UnresolvedOrders) != 1 {
		t.Fatalf("got %v", st.UnresolvedOrders)
	}

	stub.err, stub.report = nil, gridOrderReport{state: gridOrderDone}
	if err := s.Stop(context.Background()); err == nil {
		t.Fatal("want ErrBotNotRunning")
	}
	st, _ = s.GetStatus(context.Background())
	if len(st.UnresolvedOrders) != 0 {
		t.Fatalf("retry did not reconcile: %v", st.UnresolvedOrders)
	}
}

// Orders the exchange has confirmed are on the book: open-orders shows them,
// so only unknown ones are parked.
func TestParkIgnoresConfirmedOrders(t *testing.T) {
	s := NewBotService(&MockBotStatusRepository{}, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	s.parkUnknownOrders("BTCTHB", &gridPendingOrder{clientOrderID: "grid-x-2"}, nil)
	st, _ := s.GetStatus(context.Background())
	if len(st.UnresolvedOrders) != 0 {
		t.Fatalf("got %v", st.UnresolvedOrders)
	}
}

// A Stop waiter that joined run A and resumes after run B started stopping
// must not finalize B (it never joined B's loops).
func TestStaleStopWaiterDoesNotFinalizeNextRun(t *testing.T) {
	var paused atomic.Bool
	resume := make(chan struct{})
	enteredHook := make(chan struct{})
	old := afterStopJoin
	afterStopJoin = func() {
		if paused.CompareAndSwap(false, true) {
			close(enteredHook)
			<-resume
		}
	}
	t.Cleanup(func() { afterStopJoin = old })

	s := newRunningBot(t)
	releaseA := make(chan struct{})
	s.goLoop(func() { <-releaseA })

	stale := make(chan error)
	go func() { stale <- s.Stop(context.Background()) }()
	close(releaseA)
	<-enteredHook // the first waiter joined run A and is paused

	if err := s.Stop(context.Background()); err != nil { // a second Stop finalizes A
		t.Fatalf("second stop: %v", err)
	}

	// Run B starts and begins stopping; its loop is still alive.
	ctx, cancel := context.WithCancel(context.Background())
	s.runningMu.Lock()
	s.ctx, s.cancel, s.isRunning = ctx, cancel, true
	s.runLoops = &sync.WaitGroup{}
	releaseB := make(chan struct{})
	s.goLoop(func() { <-releaseB })
	s.runningMu.Unlock()
	stopB := make(chan error)
	go func() { stopB <- s.Stop(context.Background()) }()
	for {
		s.runningMu.RLock()
		pending := s.stopPending
		s.runningMu.RUnlock()
		if pending {
			break
		}
		time.Sleep(time.Millisecond)
	}

	close(resume)
	if err := <-stale; err != nil {
		t.Fatalf("stale stop: %v", err)
	}
	if err := s.Start(context.Background(), validGrid()); err == nil {
		t.Fatal("Start accepted while run B's loop is still alive")
	}

	close(releaseB)
	if err := <-stopB; err != nil {
		t.Fatalf("stop B: %v", err)
	}
}

// stallingStore never answers a write until its context ends.
type stallingStore struct{ symbolStoreRepo }

func (r *stallingStore) AddTradedSymbol(ctx context.Context, symbol, exchange string) error {
	<-ctx.Done()
	return ctx.Err()
}

// A stalled symbol write must not hold runningMu forever: Start gives up
// within the persistence timeout and Stop stays reachable.
func TestStalledSymbolPersistenceIsBounded(t *testing.T) {
	old := tradedSymbolPersistTimeout
	tradedSymbolPersistTimeout = 30 * time.Millisecond
	t.Cleanup(func() { tradedSymbolPersistTimeout = old })

	s := NewBotService(&stallingStore{}, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	done := make(chan error, 1)
	go func() { done <- s.Start(context.Background(), validGrid()) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Start must fail when the symbol write times out")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Start blocked on a stalled symbol write")
	}
	if err := s.Stop(context.Background()); !errors.Is(err, input.ErrBotNotRunning) {
		t.Fatalf("got %v, want ErrBotNotRunning", err)
	}
}

// The kill switch drains through the current exchange only, so status must
// say which exchanges symbols were traded on (a provider switch would hide
// orders left on the old one).
func TestStatusReportsExchangesSymbolsWereTradedOn(t *testing.T) {
	repo := &symbolStoreRepo{}
	before := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	before.exchangeName = func() string { return "binance" }
	if err := before.noteTradedSymbol(context.Background(), "BTCUSDT", before.currentExchange()); err != nil {
		t.Fatal(err)
	}

	after := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	after.exchangeName = func() string { return "binance_th" }
	if err := after.noteTradedSymbol(context.Background(), "BTCUSDT", after.currentExchange()); err != nil {
		t.Fatal(err)
	}
	st, err := after.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.Exchange != "binance_th" {
		t.Fatalf("exchange %q", st.Exchange)
	}
	if strings.Join(st.TradedExchanges, ",") != "binance,binance_th" {
		t.Fatalf("traded exchanges %v", st.TradedExchanges)
	}
	if strings.Join(st.Symbols, ",") != "BTCUSDT" {
		t.Fatalf("symbols %v", st.Symbols)
	}
}

// The runbook clears an old exchange by deleting its rows; the process's own
// cache must not bring it back while a store exists.
func TestDeletedTradedSymbolRowsLeaveTheStatus(t *testing.T) {
	repo := &symbolStoreRepo{}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	if err := s.noteTradedSymbol(context.Background(), "BTCUSDT", "binance"); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	repo.symbols = nil
	repo.mu.Unlock()
	st, err := s.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.TradedExchanges) != 0 || len(st.Symbols) != 0 {
		t.Fatalf("deleted rows still reported: %v %v", st.Symbols, st.TradedExchanges)
	}
}

// A row with no exchange (recorded before migration 024, or by a bot with
// no provider) is not known to be safe: report it as "unknown".
func TestUnknownTradedExchangeIsReported(t *testing.T) {
	repo := &symbolStoreRepo{}
	repo.AddTradedSymbol(context.Background(), "BTCTHB", "")
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	st, err := s.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(st.TradedExchanges, ",") != "unknown" {
		t.Fatalf("got %v", st.TradedExchanges)
	}
}

// After an operator deletes rows, the next order on that symbol must write
// its row again, or the kill switch would not drain it.
func TestSymbolRowRewrittenAfterDeletion(t *testing.T) {
	repo := &symbolStoreRepo{}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	ctx := context.Background()
	if err := s.noteTradedSymbol(ctx, "BTCTHB", "binance_th"); err != nil {
		t.Fatal(err)
	}
	repo.mu.Lock()
	repo.symbols = nil
	repo.mu.Unlock()
	if err := s.noteTradedSymbol(ctx, "BTCTHB", "binance_th"); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetStatus(ctx)
	if strings.Join(st.Symbols, ",") != "BTCTHB" {
		t.Fatalf("row not rewritten: %v", st.Symbols)
	}
}

// A run that starts and stops while the kill switch drains looks inactive
// before and after; a counter of starts tells the kill switch it happened.
func TestStatusCountsRunsStarted(t *testing.T) {
	s := NewBotService(&MockBotStatusRepository{}, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	before, _ := s.GetStatus(context.Background())
	if err := s.Start(context.Background(), validGrid()); err != nil {
		t.Fatal(err)
	}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	after, _ := s.GetStatus(context.Background())
	if after.IsActive || after.RunSeq != before.RunSeq+1 {
		t.Fatalf("run seq %d -> %d (active %v)", before.RunSeq, after.RunSeq, after.IsActive)
	}
}

// A run that starts (and stops) while the symbol list is being read must
// not be hidden: the reported counter is the one from before the read, so
// the kill switch's next status read sees it moved.
func TestStatusRunSeqPrecedesTheSymbolRead(t *testing.T) {
	repo := &symbolStoreRepo{}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	repo.onList = func() {
		s.runningMu.Lock()
		s.runSeq++ // a Start and Stop in between
		s.runningMu.Unlock()
	}
	st, err := s.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if st.RunSeq != 0 {
		t.Fatalf("reported run_seq %d, want the pre-read 0", st.RunSeq)
	}
	// It may have traded after a drain that this read is verifying.
	if !st.IsActive {
		t.Fatal("a run that started during the read must be reported active")
	}
}

// A run active (or still stopping) when the read began is reported active,
// even if it stopped before the read finished: its last symbols may be
// newer than the list.
func TestStatusActiveIfRunningWhenTheReadBegan(t *testing.T) {
	repo := &symbolStoreRepo{}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	s.isRunning = true
	repo.onList = func() {
		s.runningMu.Lock()
		s.isRunning = false
		s.runningMu.Unlock()
	}
	st, err := s.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsActive {
		t.Fatal("a run that was active at the start of the read must be reported active")
	}
}

// A Start symbol the exchange does not list is refused before it is
// recorded: every later kill would try to drain it and fail.
func TestStartRefusesUnlistedSymbolBeforeRecording(t *testing.T) {
	repo := &symbolStoreRepo{}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	s.exchangeName = func() string { return "binance_th" }
	var checked []string
	s.marketListed = func(_ context.Context, exchange, symbol string) error {
		checked = append(checked, symbol+"@"+exchange)
		return errors.New("not listed")
	}
	params := validGrid()
	if err := s.Start(context.Background(), params); err == nil {
		s.Stop(context.Background())
		t.Fatal("Start must fail for an unlisted symbol")
	}
	if len(checked) != 1 || checked[0] != params.Symbol+"@binance_th" {
		t.Fatalf("checked %v", checked)
	}
	st, err := s.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Symbols) != 0 || st.IsActive {
		t.Fatalf("unlisted symbol recorded or bot active: %v %v", st.Symbols, st.IsActive)
	}
}

// A Stop while Start waits on its market lookup wins: the Start must not
// begin trading after it, and status reports the pending Start as active
// so a kill switch never takes it for contained.
func TestStopDuringStartMarketCheckCancelsTheStart(t *testing.T) {
	repo := &symbolStoreRepo{}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	s.exchangeName = func() string { return "binance_th" }
	entered, release := make(chan struct{}), make(chan struct{})
	s.marketListed = func(context.Context, string, string) error {
		close(entered)
		<-release
		return nil
	}
	started := make(chan error, 1)
	go func() { started <- s.Start(context.Background(), validGrid()) }()
	<-entered

	st, err := s.GetStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsActive {
		t.Fatal("pending Start reported inactive")
	}
	_ = s.Stop(context.Background())
	close(release)
	if err := <-started; err == nil {
		s.Stop(context.Background())
		t.Fatal("Start went ahead after a Stop")
	}
	if s.IsRunning(context.Background()) {
		t.Fatal("bot running after Stop")
	}
}

// A Start that enters its market lookup while status reads the symbols is
// reported active by that same status read.
func TestStartPendingDuringStatusReadIsActive(t *testing.T) {
	repo := &symbolStoreRepo{}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	s.exchangeName = func() string { return "binance_th" }
	entered, release := make(chan struct{}), make(chan struct{})
	s.marketListed = func(context.Context, string, string) error {
		close(entered)
		<-release
		return errors.New("not listed")
	}
	started := make(chan error, 1)
	var once sync.Once
	repo.onList = func() {
		once.Do(func() {
			go func() { started <- s.Start(context.Background(), validGrid()) }()
			<-entered
		})
	}
	st, err := s.GetStatus(context.Background())
	close(release)
	<-started
	if err != nil {
		t.Fatal(err)
	}
	if !st.IsActive {
		t.Fatal("Start pending during the read reported inactive")
	}
}
