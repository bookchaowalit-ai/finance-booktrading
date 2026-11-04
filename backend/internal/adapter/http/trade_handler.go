package http

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"trading-bot-system/backend/internal/adapter/exchange"
	"trading-bot-system/backend/internal/config"
	"trading-bot-system/backend/internal/logger"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TradeHandler handles real trading HTTP requests
type TradeHandler struct {
	manager *exchange.ExchangeManager
	db      *pgxpool.Pool
	// recordTraded durably notes (symbol, exchange) before an order is
	// placed, so the kill switch knows every exchange orders went to.
	recordTraded func(ctx context.Context, symbol, exchange string) error
	// marketExists checks the exchange lists the symbol before it is
	// recorded; knownMarkets keeps the "provider|symbol" pairs it accepted.
	marketExists func(ctx context.Context, provider config.ExchangeProvider, symbol string) error
	knownMarkets sync.Map
}

// SetTradedSymbolRecorder makes PlaceOrder record each order's symbol and
// exchange first; an order whose record fails is not placed.
func (h *TradeHandler) SetTradedSymbolRecorder(record func(ctx context.Context, symbol, exchange string) error) {
	h.recordTraded = record
}

// NewTradeHandler creates a new trade handler
func NewTradeHandler(manager *exchange.ExchangeManager, db *pgxpool.Pool) *TradeHandler {
	h := &TradeHandler{manager: manager, db: db}
	h.marketExists = func(ctx context.Context, provider config.ExchangeProvider, symbol string) error {
		ticker, err := manager.GetTickerOn(ctx, provider, symbol)
		return listedMarket(symbol, ticker, err)
	}
	return h
}

// marketSymbol is a plain market name (BTCTHB, Bitkub's THB_BTC): the symbol
// is put into exchange URLs unescaped.
var marketSymbol = regexp.MustCompile(`^[A-Za-z0-9_]{2,30}$`)

// listedMarket accepts a ticker only when it names the requested market and
// has a price: Binance Global parses an "Invalid symbol" error body into an
// empty ticker with no error.
func listedMarket(symbol string, ticker *exchange.TickerInfo, err error) error {
	if err != nil {
		return err
	}
	if ticker == nil || !strings.EqualFold(ticker.Symbol, symbol) || ticker.LastPrice <= 0 {
		return fmt.Errorf("no ticker for %s", symbol)
	}
	return nil
}

// RegisterRoutes registers real trade routes
func (h *TradeHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/api/trade/order", h.PlaceOrder)
	mux.HandleFunc("/api/trade/balances", h.GetBalances)
	mux.HandleFunc("/api/trade/ticker", h.GetTicker)
	mux.HandleFunc("/api/trade/status", h.TradeStatus)
	mux.HandleFunc("/api/trade/history", h.TradeHistory)
	mux.HandleFunc("/api/trade/open-orders", h.GetOpenOrders)
	mux.HandleFunc("/api/trade/order-status", h.GetOrderStatus)
	mux.HandleFunc("/api/trade/cancel-order", h.CancelOrder)
	// Journal endpoints
	mux.HandleFunc("/api/journal/entry", h.JournalEntry)
	mux.HandleFunc("/api/journal/exit", h.JournalExit)
	mux.HandleFunc("/api/journal/list", h.JournalList)
	mux.HandleFunc("/api/journal/stats", h.JournalStats)
}

// ── Request/Response types ──

type RealOrderRequest struct {
	Symbol   string  `json:"symbol"`
	Side     string  `json:"side"`     // BUY or SELL
	Quantity float64 `json:"quantity"` // Base asset quantity
	Price    float64 `json:"price"`    // 0 = MARKET order, >0 = LIMIT order
}

type TradeStatusResponse struct {
	Provider     string `json:"provider"`
	HasKeys      bool   `json:"has_keys"`
	Testnet      bool   `json:"testnet"`
	ReadyToTrade bool   `json:"ready_to_trade"`
}

// PlaceOrder handles POST /api/trade/order
func (h *TradeHandler) PlaceOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req RealOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	// Validate
	if req.Symbol == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "symbol is required"})
		return
	}
	if !marketSymbol.MatchString(req.Symbol) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "symbol must be letters, digits or _"})
		return
	}
	if req.Side != "BUY" && req.Side != "SELL" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "side must be BUY or SELL"})
		return
	}
	if req.Quantity <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "quantity must be > 0"})
		return
	}

	logger.Info("Real trade order requested",
		"symbol", req.Symbol,
		"side", req.Side,
		"quantity", req.Quantity,
		"price", req.Price,
	)

	// The provider is read once: the order goes to the exchange recorded.
	provider := h.manager.GetCurrentProvider()
	if h.recordTraded != nil {
		// A recorded symbol stays in every kill switch drain, so only a
		// market the exchange lists is recorded.
		key := string(provider) + "|" + req.Symbol
		if _, known := h.knownMarkets.Load(key); !known {
			if err := h.marketExists(r.Context(), provider, req.Symbol); err != nil {
				logger.Error("Market not found, order not placed", "symbol", req.Symbol, "exchange", provider, "error", err)
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("symbol %s not available on %s, order not placed", req.Symbol, provider)})
				return
			}
			h.knownMarkets.Store(key, true)
		}
		if err := h.recordTraded(r.Context(), req.Symbol, string(provider)); err != nil {
			logger.Error("Traded symbol not recorded, order not placed", "symbol", req.Symbol, "error", err)
			// 409, not 5xx: the order was definitely not sent.
			writeJSON(w, http.StatusConflict, map[string]string{"error": "traded symbol not recorded, order not placed"})
			return
		}
	}
	result, err := h.manager.PlaceOrderOn(r.Context(), provider, req.Symbol, req.Side, req.Quantity, req.Price)
	if err != nil {
		logger.Error("Real trade order failed", "error", err)
		writeJSON(w, placeOrderErrorStatus(err), map[string]string{
			"error": err.Error(),
		})
		return
	}

	// Persist trade to database
	if h.db != nil {
		h.persistTrade(r.Context(), req, result)
	}

	logger.Info("Real trade order placed successfully",
		"symbol", req.Symbol,
		"side", req.Side,
		"result", result,
	)

	writeJSON(w, http.StatusCreated, map[string]any{
		"status": "placed",
		"order":  result,
	})
}

// GetBalances handles GET /api/trade/balances
func (h *TradeHandler) GetBalances(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	balances, err := h.manager.GetBalances(r.Context())
	if err != nil {
		logger.Error("Failed to get balances", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"balances": balances,
	})
}

// GetTicker handles GET /api/trade/ticker?symbol=BTCUSDT
func (h *TradeHandler) GetTicker(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		symbol = "BTCUSDT"
	}

	ticker, err := h.manager.GetTicker(r.Context(), symbol)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, ticker)
}

// TradeStatus handles GET /api/trade/status
func (h *TradeHandler) TradeStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	provider := string(h.manager.GetCurrentProvider())

	// Check env config keys first (in-memory apiKeys)
	hasKeys := false
	testnet := true

	// Check if we have keys configured (from env or DB)
	exchanges := h.manager.GetSupportedExchanges()
	for _, ex := range exchanges {
		if ex.Provider == provider && ex.Connected {
			hasKeys = true
			testnet = ex.Testnet
			break
		}
	}

	writeJSON(w, http.StatusOK, TradeStatusResponse{
		Provider:     provider,
		HasKeys:      hasKeys,
		Testnet:      testnet,
		ReadyToTrade: hasKeys,
	})
}

// TradeHistory handles GET /api/trade/history
func (h *TradeHandler) TradeHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.db == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := fmt.Sscanf(l, "%d", &limit); n != 1 || err != nil {
			limit = 50
		}
	}

	rows, err := h.db.Query(r.Context(),
		`SELECT id, exchange_order_id, symbol, side, type, quantity, price,
		        executed_qty, executed_price, status, fee, exchange, testnet,
		        strategy, created_at, updated_at, filled_at
		 FROM real_trades ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to load history"})
		return
	}
	defer rows.Close()

	type TradeRecord struct {
		ID              string     `json:"id"`
		ExchangeOrderID *string    `json:"exchange_order_id,omitempty"`
		Symbol          string     `json:"symbol"`
		Side            string     `json:"side"`
		Type            string     `json:"type"`
		Quantity        float64    `json:"quantity"`
		Price           float64    `json:"price"`
		ExecutedQty     float64    `json:"executed_qty"`
		ExecutedPrice   float64    `json:"executed_price"`
		Status          string     `json:"status"`
		Fee             float64    `json:"fee"`
		Exchange        string     `json:"exchange"`
		Testnet         bool       `json:"testnet"`
		Strategy        *string    `json:"strategy,omitempty"`
		CreatedAt       time.Time  `json:"created_at"`
		UpdatedAt       time.Time  `json:"updated_at"`
		FilledAt        *time.Time `json:"filled_at,omitempty"`
	}

	trades := make([]TradeRecord, 0)
	for rows.Next() {
		var t TradeRecord
		if err := rows.Scan(
			&t.ID, &t.ExchangeOrderID, &t.Symbol, &t.Side, &t.Type,
			&t.Quantity, &t.Price, &t.ExecutedQty, &t.ExecutedPrice,
			&t.Status, &t.Fee, &t.Exchange, &t.Testnet, &t.Strategy,
			&t.CreatedAt, &t.UpdatedAt, &t.FilledAt,
		); err != nil {
			logger.Error("Failed to scan trade row", "error", err)
			continue
		}
		trades = append(trades, t)
	}

	writeJSON(w, http.StatusOK, trades)
}

// persistTrade saves a real trade to the database
func (h *TradeHandler) persistTrade(ctx context.Context, req RealOrderRequest, result interface{}) {
	pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	tradeID := fmt.Sprintf("real_%d", time.Now().UnixNano())

	// Extract exchange order ID from result. The concrete type varies by
	// provider (*exchange.Order, *model.Order, ...), so round-trip through
	// JSON instead of a type assertion — a direct assertion to
	// map[string]interface{} always fails here since PlaceOrder returns a
	// typed struct pointer, never a map.
	var exchangeOrderID *string
	if raw, err := json.Marshal(result); err == nil {
		var parsed struct {
			OrderID json.Number `json:"orderId"`
		}
		if json.Unmarshal(raw, &parsed) == nil && parsed.OrderID != "" {
			s := parsed.OrderID.String()
			exchangeOrderID = &s
		}
	}

	orderType := "MARKET"
	if req.Price > 0 {
		orderType = "LIMIT"
	}

	// Determine status: MARKET orders are FILLED immediately, LIMIT orders are NEW
	status := "FILLED"
	if req.Price > 0 {
		status = "NEW"
	}

	// Look up the real testnet flag for the active provider instead of
	// hardcoding — this table must reflect whether the order actually went
	// to mainnet or testnet.
	testnet := true
	provider := string(h.manager.GetCurrentProvider())
	for _, ex := range h.manager.GetSupportedExchanges() {
		if ex.Provider == provider {
			testnet = ex.Testnet
			break
		}
	}

	_, err := h.db.Exec(pctx,
		`INSERT INTO real_trades (id, exchange_order_id, symbol, side, type, quantity, price, status, exchange, testnet, created_at, updated_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, NOW(), NOW())
		 ON CONFLICT (id) DO NOTHING`,
		tradeID, exchangeOrderID, req.Symbol, req.Side, orderType,
		req.Quantity, req.Price, status, provider, testnet,
	)
	if err != nil {
		logger.Error("Failed to persist real trade", "id", tradeID, "error", err)
	}
}

// CancelOrderRequest represents a cancel order request
type CancelOrderRequest struct {
	Symbol  string `json:"symbol"`
	OrderID int64  `json:"orderId"`
	// Exchange, when set, pins the cancel to that provider (see pinnedProvider).
	Exchange string `json:"exchange,omitempty"`
}

// pinnedProvider is the provider a request names, or the current one. The
// kill switch names the exchange it read at its start, so a provider switch
// mid-drain cannot send its lists and cancels to another exchange.
func (h *TradeHandler) pinnedProvider(name string) (config.ExchangeProvider, error) {
	if name == "" {
		return h.manager.GetCurrentProvider(), nil
	}
	switch p := config.ExchangeProvider(name); p {
	case config.ExchangeBinance, config.ExchangeBinanceTH, config.ExchangeBitkub:
		return p, nil
	default:
		return "", fmt.Errorf("unknown exchange %q", name)
	}
}

// placeOrderErrorStatus maps an order error to a status: 502 when the order
// may have reached the exchange anyway (the caller must not assume it was
// not placed), 400 when it was rejected.
func placeOrderErrorStatus(err error) int {
	if exchange.IsOrderStateUnknown(err) {
		return http.StatusBadGateway
	}
	return http.StatusBadRequest
}

// GetOpenOrders handles GET /api/trade/open-orders?symbol=BTCTHB
func (h *TradeHandler) GetOpenOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	symbol := r.URL.Query().Get("symbol")
	if symbol == "" {
		symbol = "BTCTHB"
	}

	provider, err := h.pinnedProvider(r.URL.Query().Get("exchange"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	orders, err := h.manager.GetOpenOrdersOn(r.Context(), provider, symbol)
	if err != nil {
		logger.Error("Failed to get open orders", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"symbol": symbol,
		"orders": orders,
	})
}

// GetOrderStatus handles GET /api/trade/order-status?symbol=BTCTHB&orderId=123
// Lets the strategy verify what happened to an order that vanished from the
// open-orders list (FILLED vs CANCELED/EXPIRED) before booking PnL for it.
func (h *TradeHandler) GetOrderStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	symbol := r.URL.Query().Get("symbol")
	orderIDStr := r.URL.Query().Get("orderId")
	if symbol == "" || orderIDStr == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "symbol and orderId are required"})
		return
	}

	var orderID int64
	if _, err := fmt.Sscanf(orderIDStr, "%d", &orderID); err != nil || orderID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "orderId must be a positive integer"})
		return
	}

	provider, err := h.pinnedProvider(r.URL.Query().Get("exchange"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	order, err := h.manager.GetOrderStatusOn(r.Context(), provider, symbol, orderID)
	if err != nil {
		logger.Error("Failed to get order status", "error", err, "symbol", symbol, "orderId", orderID)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, order)
}

// CancelOrder handles POST /api/trade/cancel-order
func (h *TradeHandler) CancelOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CancelOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	if req.Symbol == "" || req.OrderID == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "symbol and orderId are required"})
		return
	}

	provider, err := h.pinnedProvider(req.Exchange)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := h.manager.CancelOrderOn(r.Context(), provider, req.Symbol, req.OrderID); err != nil {
		logger.Error("Failed to cancel order", "error", err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}

	// Update status in database if we have the order ID
	if h.db != nil {
		pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		orderIDStr := fmt.Sprintf("%d", req.OrderID)
		_, _ = h.db.Exec(pctx,
			`UPDATE real_trades SET status = 'CANCELLED', updated_at = NOW() WHERE exchange_order_id = $1`,
			orderIDStr)
	}

	logger.Info("Order cancelled", "symbol", req.Symbol, "orderId", req.OrderID)

	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "cancelled",
		"orderId": req.OrderID,
	})
}

// ── Trade Journal Handlers ──────────────────────────────────────────────────

type JournalEntryRequest struct {
	Symbol            string  `json:"symbol"`
	Side              string  `json:"side"`
	Strategy          string  `json:"strategy"`
	EntryReason       string  `json:"entry_reason"`
	EntryPrice        float64 `json:"entry_price"`
	Quantity          float64 `json:"quantity"`
	ExpectedRiskTHB   float64 `json:"expected_risk_thb"`
	ExpectedRewardTHB float64 `json:"expected_reward_thb"`
	StopLossPrice     float64 `json:"stop_loss_price"`
	TakeProfitPrice   float64 `json:"take_profit_price"`
	ExchangeOrderID   string  `json:"exchange_order_id"`
	Notes             string  `json:"notes"`
}

type JournalExitRequest struct {
	ExchangeOrderID   string  `json:"exchange_order_id"`
	ExitPrice         float64 `json:"exit_price"`
	ExitReason        string  `json:"exit_reason"`
	ActualPnL         float64 `json:"actual_pnl"`
	Fee               float64 `json:"fee"`
	DrawdownImpactPct float64 `json:"drawdown_impact_pct"`
}

// JournalEntry handles POST /api/journal/entry
func (h *TradeHandler) JournalEntry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Database not available"})
		return
	}

	var req JournalEntryRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	if req.Symbol == "" || req.Side == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "symbol and side are required"})
		return
	}

	pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := h.db.Exec(pctx,
		`INSERT INTO trade_journal (symbol, side, strategy, entry_reason, entry_price, quantity,
		 expected_risk_thb, expected_reward_thb, stop_loss_price, take_profit_price,
		 exchange_order_id, status, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'OPEN', NOW())`,
		req.Symbol, req.Side, req.Strategy, req.EntryReason, req.EntryPrice,
		req.Quantity, req.ExpectedRiskTHB, req.ExpectedRewardTHB,
		req.StopLossPrice, req.TakeProfitPrice, req.ExchangeOrderID,
	)
	if err != nil {
		logger.Error("Failed to persist journal entry", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to save journal entry"})
		return
	}

	writeJSON(w, http.StatusCreated, map[string]string{"status": "recorded"})
}

// JournalExit handles POST /api/journal/exit
func (h *TradeHandler) JournalExit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.db == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Database not available"})
		return
	}

	var req JournalExitRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "Invalid request body"})
		return
	}

	if req.ExchangeOrderID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "exchange_order_id is required"})
		return
	}

	pctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	result, err := h.db.Exec(pctx,
		`UPDATE trade_journal SET
		 exit_price = $1, exit_reason = $2, actual_pnl = $3, fee = $4,
		 drawdown_impact_pct = $5, status = 'CLOSED', closed_at = NOW()
		 WHERE exchange_order_id = $6 AND status = 'OPEN'`,
		req.ExitPrice, req.ExitReason, req.ActualPnL, req.Fee,
		req.DrawdownImpactPct, req.ExchangeOrderID,
	)
	if err != nil {
		logger.Error("Failed to update journal exit", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to update journal"})
		return
	}

	rowsAffected := result.RowsAffected()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":        "updated",
		"rows_affected": rowsAffected,
	})
}

// JournalList handles GET /api/journal/list?limit=50&status=OPEN
func (h *TradeHandler) JournalList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.db == nil {
		writeJSON(w, http.StatusOK, []interface{}{})
		return
	}

	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		fmt.Sscanf(l, "%d", &limit)
	}
	status := r.URL.Query().Get("status") // optional filter

	var rows pgx.Rows
	var err error
	if status != "" {
		rows, err = h.db.Query(r.Context(),
			`SELECT id, symbol, side, strategy, entry_reason, entry_price, quantity,
			        expected_risk_thb, expected_reward_thb, exit_price, exit_reason,
			        actual_pnl, fee, drawdown_impact_pct, exchange_order_id, status,
			        created_at, closed_at
			 FROM trade_journal WHERE status = $1 ORDER BY created_at DESC LIMIT $2`,
			status, limit)
	} else {
		rows, err = h.db.Query(r.Context(),
			`SELECT id, symbol, side, strategy, entry_reason, entry_price, quantity,
			        expected_risk_thb, expected_reward_thb, exit_price, exit_reason,
			        actual_pnl, fee, drawdown_impact_pct, exchange_order_id, status,
			        created_at, closed_at
			 FROM trade_journal ORDER BY created_at DESC LIMIT $1`, limit)
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to load journal"})
		return
	}
	defer rows.Close()

	type JournalRecord struct {
		ID                int64      `json:"id"`
		Symbol            string     `json:"symbol"`
		Side              string     `json:"side"`
		Strategy          string     `json:"strategy"`
		EntryReason       string     `json:"entry_reason"`
		EntryPrice        float64    `json:"entry_price"`
		Quantity          float64    `json:"quantity"`
		ExpectedRiskTHB   float64    `json:"expected_risk_thb"`
		ExpectedRewardTHB float64    `json:"expected_reward_thb"`
		ExitPrice         float64    `json:"exit_price"`
		ExitReason        string     `json:"exit_reason"`
		ActualPnL         float64    `json:"actual_pnl"`
		Fee               float64    `json:"fee"`
		DrawdownImpactPct float64    `json:"drawdown_impact_pct"`
		ExchangeOrderID   string     `json:"exchange_order_id"`
		Status            string     `json:"status"`
		CreatedAt         time.Time  `json:"created_at"`
		ClosedAt          *time.Time `json:"closed_at,omitempty"`
	}

	entries := make([]JournalRecord, 0)
	for rows.Next() {
		var e JournalRecord
		if err := rows.Scan(
			&e.ID, &e.Symbol, &e.Side, &e.Strategy, &e.EntryReason,
			&e.EntryPrice, &e.Quantity, &e.ExpectedRiskTHB, &e.ExpectedRewardTHB,
			&e.ExitPrice, &e.ExitReason, &e.ActualPnL, &e.Fee,
			&e.DrawdownImpactPct, &e.ExchangeOrderID, &e.Status,
			&e.CreatedAt, &e.ClosedAt,
		); err != nil {
			logger.Error("Failed to scan journal row", "error", err)
			continue
		}
		entries = append(entries, e)
	}

	writeJSON(w, http.StatusOK, entries)
}

// JournalStats handles GET /api/journal/stats
func (h *TradeHandler) JournalStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.db == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"total_entries": 0, "winning_trades": 0, "losing_trades": 0,
			"win_rate": 0.0, "total_pnl": 0.0, "total_fees": 0.0,
		})
		return
	}

	var totalEntries, winningTrades, losingTrades int
	var totalPnL, totalFees float64

	err := h.db.QueryRow(r.Context(),
		`SELECT
		 COUNT(*) FILTER (WHERE status = 'CLOSED') as total_closed,
		 COUNT(*) FILTER (WHERE status = 'CLOSED' AND actual_pnl > 0) as wins,
		 COUNT(*) FILTER (WHERE status = 'CLOSED' AND actual_pnl <= 0) as losses,
		 COALESCE(SUM(actual_pnl) FILTER (WHERE status = 'CLOSED'), 0) as total_pnl,
		 COALESCE(SUM(fee) FILTER (WHERE status = 'CLOSED'), 0) as total_fees
		 FROM trade_journal`,
	).Scan(&totalEntries, &winningTrades, &losingTrades, &totalPnL, &totalFees)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Failed to compute stats"})
		return
	}

	winRate := 0.0
	if totalEntries > 0 {
		winRate = float64(winningTrades) / float64(totalEntries) * 100
	}

	// Count open entries
	var openEntries int
	h.db.QueryRow(r.Context(),
		`SELECT COUNT(*) FROM trade_journal WHERE status = 'OPEN'`,
	).Scan(&openEntries)

	writeJSON(w, http.StatusOK, map[string]any{
		"total_entries":  totalEntries + openEntries,
		"open_entries":   openEntries,
		"closed_entries": totalEntries,
		"winning_trades": winningTrades,
		"losing_trades":  losingTrades,
		"win_rate":       roundTo(winRate, 1),
		"total_pnl":      roundTo(totalPnL, 2),
		"total_fees":     roundTo(totalFees, 2),
	})
}

func roundTo(val float64, places int) float64 {
	mul := 1.0
	for i := 0; i < places; i++ {
		mul *= 10
	}
	return float64(int(val*mul+0.5)) / mul
}
