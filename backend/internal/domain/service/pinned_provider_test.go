package service

import (
	"context"
	"errors"
	"testing"

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
	if _, err := placeOnPinnedProvider(context.Background(), p, record, "BTCUSDT", "grid-x-1", "BUY", 1, 100); err != nil {
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
	if _, err := placeOnPinnedProvider(context.Background(), p, record, "BTCTHB", "grid-x-2", "BUY", 1, 100); err == nil {
		t.Fatal("want an error")
	} else if exchange.IsOrderStateUnknown(err) {
		t.Fatal("a refused submission is not an unknown outcome")
	}
	if len(p.placedOn) != 0 {
		t.Fatalf("order placed without a record: %v", p.placedOn)
	}
}
