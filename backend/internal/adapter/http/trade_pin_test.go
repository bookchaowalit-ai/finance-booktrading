package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"trading-bot-system/backend/internal/adapter/exchange"
	"trading-bot-system/backend/internal/config"
)

// No API keys: every provider answers with its own "not available" error,
// which shows which provider a request reached.
func pinTestHandler() *TradeHandler {
	m := exchange.NewExchangeManager(&config.ExchangeConfig{Provider: config.ExchangeBinanceTH, APIKeys: map[string]*config.ExchangeAPIKey{}}, nil)
	return NewTradeHandler(m, nil)
}

func errorOf(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body["error"]
}

// A request naming an exchange reaches that exchange, not the current one.
func TestTradeRequestsGoToTheNamedExchange(t *testing.T) {
	h := pinTestHandler()

	rec := httptest.NewRecorder()
	h.GetOpenOrders(rec, httptest.NewRequest(http.MethodGet, "/api/trade/open-orders?symbol=BTCUSDT&exchange=binance", nil))
	if !strings.Contains(errorOf(t, rec), "Binance open orders") {
		t.Fatalf("open orders went elsewhere: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.GetOrderStatus(rec, httptest.NewRequest(http.MethodGet, "/api/trade/order-status?symbol=BTCUSDT&orderId=5&exchange=binance", nil))
	if !strings.Contains(errorOf(t, rec), "exchange: binance") {
		t.Fatalf("order status went elsewhere: %d %s", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	h.CancelOrder(rec, httptest.NewRequest(http.MethodPost, "/api/trade/cancel-order", strings.NewReader(`{"symbol":"BTCUSDT","orderId":5,"exchange":"binance"}`)))
	if !strings.Contains(errorOf(t, rec), "exchange: binance") {
		t.Fatalf("cancel went elsewhere: %d %s", rec.Code, rec.Body)
	}

	// Without a name: the current provider, as before.
	rec = httptest.NewRecorder()
	h.GetOpenOrders(rec, httptest.NewRequest(http.MethodGet, "/api/trade/open-orders?symbol=BTCTHB", nil))
	if !strings.Contains(errorOf(t, rec), "Binance TH") {
		t.Fatalf("unnamed request: %d %s", rec.Code, rec.Body)
	}
}

func TestTradeRequestRejectsUnknownExchange(t *testing.T) {
	rec := httptest.NewRecorder()
	pinTestHandler().GetOpenOrders(rec, httptest.NewRequest(http.MethodGet, "/api/trade/open-orders?symbol=BTCTHB&exchange=nope", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("got %d", rec.Code)
	}
}

// Trade-API orders (the strategy's grid) record their symbol and exchange
// before they are sent, so the kill switch sees every exchange used.
func TestPlaceOrderRecordsItsExchangeFirst(t *testing.T) {
	h := pinTestHandler()
	var recorded []string
	h.SetTradedSymbolRecorder(func(ctx context.Context, symbol, exchange string) error {
		recorded = append(recorded, symbol+"@"+exchange)
		return nil
	})
	rec := httptest.NewRecorder()
	h.PlaceOrder(rec, httptest.NewRequest(http.MethodPost, "/api/trade/order", strings.NewReader(`{"symbol":"BTCUSDT","side":"BUY","quantity":1,"price":100}`)))
	if len(recorded) != 1 || recorded[0] != "BTCUSDT@binance_th" {
		t.Fatalf("recorded %v", recorded)
	}
}

// No record, no order, and a definite (non-5xx) refusal.
func TestPlaceOrderRefusedWhenNotRecorded(t *testing.T) {
	h := pinTestHandler()
	h.SetTradedSymbolRecorder(func(context.Context, string, string) error { return errors.New("db down") })
	rec := httptest.NewRecorder()
	h.PlaceOrder(rec, httptest.NewRequest(http.MethodPost, "/api/trade/order", strings.NewReader(`{"symbol":"BTCUSDT","side":"BUY","quantity":1,"price":100}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}
