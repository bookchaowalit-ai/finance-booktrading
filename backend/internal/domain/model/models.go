package model

import "time"

// TradeSymbol represents a trading pair
type TradeSymbol string

const (
	BTCUSDT TradeSymbol = "BTCUSDT"
	ETHUSDT TradeSymbol = "ETHUSDT"
	BTCTHB  TradeSymbol = "BTCTHB"
	ETHTHB  TradeSymbol = "ETHTHB"
)

// OrderSide represents buy or sell
type OrderSide string

const (
	SideBuy  OrderSide = "BUY"
	SideSell OrderSide = "SELL"
)

// OrderType represents the type of order
type OrderType string

const (
	OrderTypeMarket OrderType = "MARKET"
	OrderTypeLimit  OrderType = "LIMIT"
)

// Order represents a trading order
type Order struct {
	ID        string      `json:"id"`
	Symbol    TradeSymbol `json:"symbol"`
	Side      OrderSide   `json:"side"`
	Type      OrderType   `json:"type"`
	Quantity  float64     `json:"quantity"`
	Price     float64     `json:"price,omitempty"`
	Status    OrderStatus `json:"status"`
	CreatedAt time.Time   `json:"created_at"`
	UpdatedAt time.Time   `json:"updated_at"`
}

// OrderStatus represents the status of an order
type OrderStatus string

const (
	OrderStatusPending   OrderStatus = "PENDING"
	OrderStatusFilled    OrderStatus = "FILLED"
	OrderStatusCancelled OrderStatus = "CANCELLED"
	OrderStatusRejected  OrderStatus = "REJECTED"
)

// MarketData represents real-time market data
type MarketData struct {
	Symbol    TradeSymbol `json:"symbol"`
	Price     float64     `json:"price"`
	Volume    float64     `json:"volume"`
	Timestamp time.Time   `json:"timestamp"`
}

// Portfolio represents user's trading portfolio
type Portfolio struct {
	Symbol      TradeSymbol `json:"symbol"`
	Balance     float64     `json:"balance"`
	Locked      float64     `json:"locked"`
	AvgBuyPrice float64     `json:"avg_buy_price"`
	UpdatedAt   time.Time   `json:"updated_at"`
}

// TradeHistory represents a completed trade
type TradeHistory struct {
	ID         string      `json:"id"`
	Symbol     TradeSymbol `json:"symbol"`
	Side       OrderSide   `json:"side"`
	Quantity   float64     `json:"quantity"`
	Price      float64     `json:"price"`
	Total      float64     `json:"total"`
	Fee        float64     `json:"fee"`
	ExecutedAt time.Time   `json:"executed_at"`
}

// BotStatus represents the trading bot status
type BotStatus struct {
	IsActive    bool       `json:"is_active"`
	StartedAt   *time.Time `json:"started_at,omitempty"`
	StoppedAt   *time.Time `json:"stopped_at,omitempty"`
	TotalTrades int        `json:"total_trades"`
	TotalProfit float64    `json:"total_profit"`
	BotMode     BotMode    `json:"bot_mode,omitempty"`
	// Symbol is the symbol the running grid/auto loop trades; the strategy
	// kill switch drains its open orders too.
	Symbol string `json:"symbol,omitempty"`
	// Symbols lists every symbol any run traded since the backend started,
	// so a kill (or retry) drains all of them.
	Symbols []string `json:"symbols,omitempty"`
}

// OrderRequest represents an incoming order request
type OrderRequest struct {
	Symbol   TradeSymbol `json:"symbol"`
	Side     OrderSide   `json:"side"`
	Quantity float64     `json:"quantity"`
	Price    float64     `json:"price,omitempty"`
}

// OrderResponse represents the response after creating an order
type OrderResponse struct {
	OrderID   string      `json:"order_id"`
	Symbol    TradeSymbol `json:"symbol"`
	Side      OrderSide   `json:"side"`
	Quantity  float64     `json:"quantity"`
	Status    OrderStatus `json:"status"`
	CreatedAt time.Time   `json:"created_at"`
}

// TradeNotification represents a real-time trade execution notification
type TradeNotification struct {
	ID        string      `json:"id"`
	Symbol    TradeSymbol `json:"symbol"`
	Side      OrderSide   `json:"side"`
	Quantity  float64     `json:"quantity"`
	Price     float64     `json:"price"`
	Total     float64     `json:"total"`
	Type      string      `json:"type"` // "GRID_BUY", "GRID_SELL", "SIGNAL_BUY", "SIGNAL_SELL"
	Timestamp time.Time   `json:"timestamp"`
	Message   string      `json:"message"`
}

// BotActivity represents a real-time bot activity update
type BotActivity struct {
	Timestamp time.Time `json:"timestamp"`
	Activity  string    `json:"activity"` // "SCANNING", "ANALYZING", "WAITING", "PLACING_ORDER", "ERROR"
	Symbol    string    `json:"symbol,omitempty"`
	Message   string    `json:"message"`
	Level     string    `json:"level"` // "info", "success", "warning", "error"
}

// BotMode represents the current bot operating mode
type BotMode string

const (
	BotModeGrid   BotMode = "GRID"
	BotModeSignal BotMode = "SIGNAL"
	BotModeAuto   BotMode = "AUTO"
)
