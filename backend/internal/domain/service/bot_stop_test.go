package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"trading-bot-system/backend/internal/port/input"
)

func TestStopWhenNotRunningReturnsSentinel(t *testing.T) {
	s := &BotServiceImpl{}
	if err := s.Stop(context.Background()); !errors.Is(err, input.ErrBotNotRunning) {
		t.Fatalf("got %v, want input.ErrBotNotRunning", err)
	}
}

func newRunningBot(t *testing.T) *BotServiceImpl {
	t.Helper()
	s := NewBotService(&MockBotStatusRepository{}, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel, s.isRunning = ctx, cancel, true
	return s
}

// The kill switch drains exchange orders right after Stop returns, so Stop
// must not return while a loop could still be submitting an order.
func TestStopWaitsForTradingLoopToExit(t *testing.T) {
	s := newRunningBot(t)
	exited := make(chan struct{})
	s.goLoop(func() {
		<-s.ctx.Done()
		time.Sleep(50 * time.Millisecond) // an order submission finishing
		close(exited)
	})

	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("stop: %v", err)
	}
	select {
	case <-exited:
	default:
		t.Fatal("Stop returned before the trading loop exited")
	}
}

func TestStopFailsWhenLoopDoesNotExit(t *testing.T) {
	old := botStopJoinTimeout
	botStopJoinTimeout = 30 * time.Millisecond
	t.Cleanup(func() { botStopJoinTimeout = old })

	s := newRunningBot(t)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	s.goLoop(func() { <-release })

	if err := s.Stop(context.Background()); !errors.Is(err, ErrBotStopTimeout) {
		t.Fatalf("got %v, want ErrBotStopTimeout", err)
	}
}

// After a timed-out Stop, a retry must not answer "not running" (which the
// kill switch treats as stopped) while the old loop is still alive.
func TestRetriedStopAfterTimeoutStillReportsTimeout(t *testing.T) {
	old := botStopJoinTimeout
	botStopJoinTimeout = 30 * time.Millisecond
	t.Cleanup(func() { botStopJoinTimeout = old })

	s := newRunningBot(t)
	release := make(chan struct{})
	s.goLoop(func() { <-release })

	if err := s.Stop(context.Background()); !errors.Is(err, ErrBotStopTimeout) {
		t.Fatalf("first stop: got %v", err)
	}
	if err := s.Stop(context.Background()); !errors.Is(err, ErrBotStopTimeout) {
		t.Fatalf("retried stop while loop alive: got %v, want ErrBotStopTimeout", err)
	}
	if err := s.Start(context.Background(), validGrid()); err == nil {
		t.Fatal("Start must be refused while the previous run is still stopping")
	}

	close(release)
	if err := s.Stop(context.Background()); err != nil {
		t.Fatalf("stop after the loop exited must complete the stop: %v", err)
	}
	if err := s.Stop(context.Background()); !errors.Is(err, input.ErrBotNotRunning) {
		t.Fatalf("once stopped: got %v, want ErrBotNotRunning", err)
	}
}

// blockingStatusRepo parks SetActive(false) until released, to hold Stop in
// its finalization step.
type blockingStatusRepo struct {
	MockBotStatusRepository
	entered chan struct{}
	release chan struct{}
}

func (b *blockingStatusRepo) SetActive(ctx context.Context, active bool) error {
	if !active {
		close(b.entered)
		<-b.release
	}
	return nil
}

func TestStartBlockedUntilStopFinalizes(t *testing.T) {
	repo := &blockingStatusRepo{entered: make(chan struct{}), release: make(chan struct{})}
	s := NewBotService(repo, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	ctx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel, s.isRunning = ctx, cancel, true

	done := make(chan error)
	go func() { done <- s.Stop(context.Background()) }()
	<-repo.entered
	if err := s.Start(context.Background(), validGrid()); err == nil {
		t.Fatal("Start must wait until Stop has finished writing the stopped status")
	}
	close(repo.release)
	if err := <-done; err != nil {
		t.Fatalf("stop: %v", err)
	}
}

// The kill switch drains the Go bot's symbol too, so status must report it.
func TestStatusReportsRunningSymbol(t *testing.T) {
	s := NewBotService(&MockBotStatusRepository{}, &MockRedisPublisher{}, &MockWebSocketBroadcaster{})
	s.isRunning, s.symbol = true, "BTCUSDT"
	st, err := s.GetStatus(context.Background())
	if err != nil || st.Symbol != "BTCUSDT" {
		t.Fatalf("got %+v err=%v, want symbol BTCUSDT", st, err)
	}
}

// Signal mode can trade any symbol, and a kill retry runs after the bot has
// stopped: status must list every symbol traded in the last run, even then.
func TestStatusListsTradedSymbolsAfterStop(t *testing.T) {
	s := newRunningBot(t)
	s.noteTradedSymbol("ETHUSDT")
	s.noteTradedSymbol("BTCUSDT")
	s.noteTradedSymbol("ETHUSDT")
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	st, _ := s.GetStatus(context.Background())
	if strings.Join(st.Symbols, ",") != "BTCUSDT,ETHUSDT" {
		t.Fatalf("got %v", st.Symbols)
	}
	if err := s.Start(context.Background(), validGrid()); err != nil {
		t.Fatal(err)
	}
	st, _ = s.GetStatus(context.Background())
	if strings.Join(st.Symbols, ",") != "BTCUSDT" { // validGrid trades BTCUSDT; old run's list is gone
		t.Fatalf("after Start got %v", st.Symbols)
	}
	s.Stop(context.Background())
}
