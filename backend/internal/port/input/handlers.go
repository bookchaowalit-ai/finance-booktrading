package input

import (
	"context"
	"errors"

	"trading-bot-system/backend/internal/domain/model"
)

// OrderHandler defines the interface for handling order-related requests
type OrderHandler interface {
	CreateOrder(ctx context.Context, req *model.OrderRequest) (*model.OrderResponse, error)
	CancelOrder(ctx context.Context, orderID string) error
	GetOrder(ctx context.Context, orderID string) (*model.Order, error)
	GetAllOrders(ctx context.Context) ([]*model.Order, error)
	GetOpenOrders(ctx context.Context) ([]*model.Order, error)
}

// MarketDataHandler defines the interface for handling market data requests
type MarketDataHandler interface {
	GetLatestPrice(ctx context.Context, symbol model.TradeSymbol) (*model.MarketData, error)
	GetPriceHistory(ctx context.Context, symbol model.TradeSymbol) ([]*model.MarketData, error)
	Subscribe(ctx context.Context, symbol model.TradeSymbol) (<-chan *model.MarketData, error)
	Unsubscribe(ctx context.Context, symbol model.TradeSymbol) error
	StartStreaming(ctx context.Context, symbol model.TradeSymbol) error
	StopStreaming(ctx context.Context, symbol model.TradeSymbol) error
	Shutdown()
}

// PortfolioHandler defines the interface for handling portfolio requests
type PortfolioHandler interface {
	GetPortfolio(ctx context.Context) ([]*model.Portfolio, error)
}

// BotStartParams contains parameters for starting a trading bot
type BotStartParams struct {
	Symbol       string
	Quantity     float64
	GridLevels   int
	LowerPrice   float64
	UpperPrice   float64
	Investment   float64
	BotMode      string       // "GRID", "SIGNAL", "AUTO"
	SignalConfig SignalConfig // configuration for SIGNAL/AUTO modes
}

// SignalConfig holds configuration for signal-driven and auto bot modes
type SignalConfig struct {
	Symbol         string  `json:"symbol"`           // trading symbol (default BTCUSDT)
	RiskLevel      string  `json:"risk_level"`       // "conservative", "moderate", "aggressive"
	MaxPositionPct float64 `json:"max_position_pct"` // max portfolio allocation per trade (0.0-1.0)
	StopLossPct    float64 `json:"stop_loss_pct"`    // stop-loss percentage
	TakeProfitPct  float64 `json:"take_profit_pct"`  // take-profit percentage
	MinStrength    float64 `json:"min_strength"`     // minimum signal strength (0.0-1.0)
	Quantity       float64 `json:"quantity"`         // trade quantity per signal
}

// ErrBotNotRunning is returned by BotHandler.Stop when no bot loop is
// running. Callers that only need the bot stopped (the strategy kill switch)
// treat it as success.
var ErrBotNotRunning = errors.New("bot is not running")

// BotHandler defines the interface for handling bot control requests
type BotHandler interface {
	Start(ctx context.Context, params *BotStartParams) error
	Stop(ctx context.Context) error
	GetStatus(ctx context.Context) (*model.BotStatus, error)
}

// TradeHistoryHandler defines the interface for handling trade history requests
type TradeHistoryHandler interface {
	GetTradeHistory(ctx context.Context, limit int) ([]*model.TradeHistory, error)
	GetTradeHistoryBySymbol(ctx context.Context, symbol model.TradeSymbol, limit int) ([]*model.TradeHistory, error)
}
