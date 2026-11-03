package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"trading-bot-system/backend/internal/domain/model"
	"trading-bot-system/backend/internal/port/input"
)

type stoppedBot struct{}

func (stoppedBot) Start(context.Context, *input.BotStartParams) error { return nil }
func (stoppedBot) Stop(context.Context) error                         { return input.ErrBotNotRunning }
func (stoppedBot) GetStatus(context.Context) (*model.BotStatus, error) {
	return &model.BotStatus{}, nil
}

// The strategy kill switch stops the Go loop and treats "already stopped" as
// success, so that case must be a distinct 409, not a generic error.
func TestStopWhenNotRunningIsConflict(t *testing.T) {
	handlers := map[string]func(http.ResponseWriter, *http.Request){
		"/api/bot/stop":     NewBotHandler(stoppedBot{}).StopBot,
		"/api/trading/stop": NewTradingHandler(nil, stoppedBot{}).StopBot,
	}
	for path, h := range handlers {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code != http.StatusConflict {
			t.Errorf("%s: got %d, want 409", path, rec.Code)
		}
	}
}
