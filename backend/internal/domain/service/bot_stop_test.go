package service

import (
	"context"
	"errors"
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
