package service

import (
	"context"
	"errors"
	"testing"

	"trading-bot-system/backend/internal/port/input"
)

func TestStopWhenNotRunningReturnsSentinel(t *testing.T) {
	s := &BotServiceImpl{}
	if err := s.Stop(context.Background()); !errors.Is(err, input.ErrBotNotRunning) {
		t.Fatalf("got %v, want input.ErrBotNotRunning", err)
	}
}
