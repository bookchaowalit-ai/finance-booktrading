package repository

import (
	"context"
	"time"

	"trading-bot-system/backend/internal/domain/model"
)

// OrderRepository defines the interface for order persistence
type OrderRepository interface {
	Create(ctx context.Context, order *model.Order) error
	GetByID(ctx context.Context, id string) (*model.Order, error)
	GetBySymbol(ctx context.Context, symbol model.TradeSymbol) ([]*model.Order, error)
	UpdateStatus(ctx context.Context, id string, status model.OrderStatus) error
	GetAll(ctx context.Context) ([]*model.Order, error)
}

// PortfolioRepository defines the interface for portfolio persistence
type PortfolioRepository interface {
	Get(ctx context.Context, symbol model.TradeSymbol) (*model.Portfolio, error)
	Update(ctx context.Context, portfolio *model.Portfolio) error
	GetAll(ctx context.Context) ([]*model.Portfolio, error)
}

// TradeHistoryRepository defines the interface for trade history persistence
type TradeHistoryRepository interface {
	Add(ctx context.Context, trade *model.TradeHistory) error
	GetAll(ctx context.Context, limit int) ([]*model.TradeHistory, error)
	GetBySymbol(ctx context.Context, symbol model.TradeSymbol, limit int) ([]*model.TradeHistory, error)
}

// BotStatusRepository defines the interface for bot status persistence
type BotStatusRepository interface {
	Get(ctx context.Context) (*model.BotStatus, error)
	SetActive(ctx context.Context, active bool) error
	IncrementTrades(ctx context.Context) error
	UpdateProfit(ctx context.Context, profit float64) error
}

// TradedSymbolStore persists every symbol the bot has traded, so the kill
// switch still drains their open orders after a backend restart. A bot status
// repository may implement it.
type TradedSymbolStore interface {
	AddTradedSymbol(ctx context.Context, symbol, exchange string) error
	ListTradedSymbols(ctx context.Context) ([]model.TradedSymbol, error)
}

// MarketDataRepository defines the interface for market data caching
type MarketDataRepository interface {
	GetLatest(ctx context.Context, symbol model.TradeSymbol) (*model.MarketData, error)
	Save(ctx context.Context, data *model.MarketData) error
	GetPriceHistory(ctx context.Context, symbol model.TradeSymbol, duration time.Duration) ([]*model.MarketData, error)
}
