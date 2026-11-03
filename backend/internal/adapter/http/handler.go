package http

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"trading-bot-system/backend/internal/domain/model"
	"trading-bot-system/backend/internal/logger"
	"trading-bot-system/backend/internal/port/input"
)

// OrderHandler handles HTTP requests for order operations
type OrderHandler struct {
	orderHandler input.OrderHandler
}

// NewOrderHandler creates a new order HTTP handler
func NewOrderHandler(handler input.OrderHandler) *OrderHandler {
	return &OrderHandler{
		orderHandler: handler,
	}
}

// CreateOrder handles POST /api/orders
func (h *OrderHandler) CreateOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req model.OrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	response, err := h.orderHandler.CreateOrder(r.Context(), &req)
	if err != nil {
		respondError(w, "Internal server error", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(response)
}

// GetOrder handles GET /api/orders/{id}
func (h *OrderHandler) GetOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract order ID from URL path - simple implementation
	// In production, use a router like chi or gorilla/mux
	orderID := r.URL.Path[len("/api/orders/"):]
	if orderID == "" {
		http.Error(w, "Order ID required", http.StatusBadRequest)
		return
	}

	order, err := h.orderHandler.GetOrder(r.Context(), orderID)
	if err != nil {
		respondError(w, "Not found", err, http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(order)
}

// GetOpenOrders handles GET /api/orders/open
func (h *OrderHandler) GetOpenOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	orders, err := h.orderHandler.GetOpenOrders(r.Context())
	if err != nil {
		respondError(w, "Internal server error", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(orders)
}

// GetAllOrders handles GET /api/orders
func (h *OrderHandler) GetAllOrders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	orders, err := h.orderHandler.GetAllOrders(r.Context())
	if err != nil {
		respondError(w, "Internal server error", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(orders)
}

// CancelOrder handles DELETE /api/orders/{id}
func (h *OrderHandler) CancelOrder(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Extract order ID from URL path
	orderID := r.URL.Path[len("/api/orders/"):]
	if orderID == "" {
		http.Error(w, "Order ID required", http.StatusBadRequest)
		return
	}

	if err := h.orderHandler.CancelOrder(r.Context(), orderID); err != nil {
		respondError(w, "Internal server error", err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// BotHandler handles HTTP requests for bot operations
type BotHandler struct {
	botHandler input.BotHandler
}

// NewBotHandler creates a new bot HTTP handler
func NewBotHandler(handler input.BotHandler) *BotHandler {
	return &BotHandler{
		botHandler: handler,
	}
}

// StartBot handles POST /api/bot/start
func (h *BotHandler) StartBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Try to parse optional grid trading parameters
	var params *input.BotStartParams
	var reqBody map[string]interface{}
	if r.Body != nil {
		json.NewDecoder(r.Body).Decode(&reqBody)
	}

	if reqBody != nil {
		params = &input.BotStartParams{}

		if v, ok := reqBody["symbol"].(string); ok {
			params.Symbol = v
		}
		if v, ok := reqBody["quantity"].(float64); ok {
			params.Quantity = v
		}
		if v, ok := reqBody["gridLevels"].(float64); ok {
			params.GridLevels = int(v)
		}
		if v, ok := reqBody["lowerPrice"].(float64); ok {
			params.LowerPrice = v
		}
		if v, ok := reqBody["upperPrice"].(float64); ok {
			params.UpperPrice = v
		}
		if v, ok := reqBody["investment"].(float64); ok {
			params.Investment = v
		}
		if v, ok := reqBody["botMode"].(string); ok {
			params.BotMode = v
		}

		// Parse signal config (nested object)
		if cfg, ok := reqBody["signalConfig"].(map[string]interface{}); ok {
			if v, ok := cfg["symbol"].(string); ok {
				params.SignalConfig.Symbol = v
			}
			if v, ok := cfg["riskLevel"].(string); ok {
				params.SignalConfig.RiskLevel = v
			}
			if v, ok := cfg["maxPositionPct"].(float64); ok {
				params.SignalConfig.MaxPositionPct = v
			}
			if v, ok := cfg["stopLossPct"].(float64); ok {
				params.SignalConfig.StopLossPct = v
			}
			if v, ok := cfg["takeProfitPct"].(float64); ok {
				params.SignalConfig.TakeProfitPct = v
			}
			if v, ok := cfg["minStrength"].(float64); ok {
				params.SignalConfig.MinStrength = v
			}
			if v, ok := cfg["quantity"].(float64); ok {
				params.SignalConfig.Quantity = v
			}
		}
	}

	if err := h.botHandler.Start(r.Context(), params); err != nil {
		respondError(w, "Internal server error", err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "started"})
}

// StopBot handles POST /api/bot/stop
func (h *BotHandler) StopBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := h.botHandler.Stop(r.Context()); err != nil {
		if errors.Is(err, input.ErrBotNotRunning) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		respondError(w, "Internal server error", err, http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{"status": "stopped"})
}

// GetBotStatus handles GET /api/bot/status
func (h *BotHandler) GetBotStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status, err := h.botHandler.GetStatus(r.Context())
	if err != nil {
		respondError(w, "Internal server error", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// PortfolioHandler handles HTTP requests for portfolio operations
type PortfolioHandler struct {
	portfolioHandler input.PortfolioHandler
}

// NewPortfolioHandler creates a new portfolio HTTP handler
func NewPortfolioHandler(handler input.PortfolioHandler) *PortfolioHandler {
	return &PortfolioHandler{
		portfolioHandler: handler,
	}
}

// GetPortfolio handles GET /api/portfolio
func (h *PortfolioHandler) GetPortfolio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	portfolio, err := h.portfolioHandler.GetPortfolio(r.Context())
	if err != nil {
		respondError(w, "Internal server error", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(portfolio)
}

// TradeHistoryHandler handles HTTP requests for trade history
type TradeHistoryHandler struct {
	tradeHistoryHandler input.TradeHistoryHandler
}

// NewTradeHistoryHandler creates a new trade history HTTP handler
func NewTradeHistoryHandler(handler input.TradeHistoryHandler) *TradeHistoryHandler {
	return &TradeHistoryHandler{
		tradeHistoryHandler: handler,
	}
}

// GetTradeHistory handles GET /api/trades
func (h *TradeHistoryHandler) GetTradeHistory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limitStr := r.URL.Query().Get("limit")
	limit := 50 // default limit
	if limitStr != "" {
		if parsed, err := strconv.Atoi(limitStr); err == nil && parsed > 0 {
			limit = parsed
		}
	}

	trades, err := h.tradeHistoryHandler.GetTradeHistory(r.Context(), limit)
	if err != nil {
		respondError(w, "Internal server error", err, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(trades)
}

// HealthHandler handles health check requests
type HealthHandler struct{}

// NewHealthHandler creates a new health handler
func NewHealthHandler() *HealthHandler {
	return &HealthHandler{}
}

// HealthCheck handles GET /api/health
func (h *HealthHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "healthy",
	})
}

// Router configures HTTP routes
type Router struct {
	mux          *http.ServeMux
	authHandler  *AuthHandler
	serviceToken string // strategy AUTH_TOKEN; accepted on Service routes only
}

// NewRouter creates a new router
func NewRouter(authHandler *AuthHandler) *Router {
	return &Router{
		mux:         http.NewServeMux(),
		authHandler: authHandler,
	}
}

// Mux returns the underlying ServeMux for additional route registration
func (r *Router) Mux() *http.ServeMux {
	return r.mux
}

// RegisterOrderRoutes registers order-related routes
func (r *Router) RegisterOrderRoutes(handler *OrderHandler) {
	r.mux.HandleFunc("/api/orders/open", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetOpenOrders(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/orders", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPost:
			handler.CreateOrder(w, req)
		case http.MethodGet:
			handler.GetAllOrders(w, req)
		case http.MethodDelete:
			handler.CancelOrder(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/orders/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetOrder(w, req)
		} else if req.Method == http.MethodDelete {
			handler.CancelOrder(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterBotRoutes registers bot-related routes
func (r *Router) RegisterBotRoutes(handler *BotHandler) {
	r.mux.HandleFunc("/api/bot/start", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.StartBot(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/bot/stop", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.StopBot(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/bot/status", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetBotStatus(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterPortfolioRoutes registers portfolio-related routes
func (r *Router) RegisterPortfolioRoutes(handler *PortfolioHandler) {
	r.mux.HandleFunc("/api/portfolio", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetPortfolio(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterTradeHistoryRoutes registers trade history routes
func (r *Router) RegisterTradeHistoryRoutes(handler *TradeHistoryHandler) {
	r.mux.HandleFunc("/api/trades", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetTradeHistory(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterHealthRoute registers health check route
func (r *Router) RegisterHealthRoute(handler *HealthHandler) {
	r.mux.HandleFunc("/api/health", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.HealthCheck(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterExchangeRoutes registers exchange-related routes
func (r *Router) RegisterExchangeRoutes(handler *ExchangeHandler) {
	r.mux.HandleFunc("/api/exchange", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetExchanges(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/exchange/set", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.SetExchange(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/exchange/configure", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetConfiguredExchanges(w, req)
		} else if req.Method == http.MethodPost {
			handler.ConfigureExchange(w, req)
		} else if req.Method == http.MethodDelete {
			handler.DeleteExchange(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/exchange/balances", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetBalances(w, req)
		} else if req.Method == http.MethodPost {
			handler.RefreshBalances(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/exchange/all-balances", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetAllBalances(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterSettingsRoutes registers settings-related routes
func (r *Router) RegisterSettingsRoutes(handler *SettingsHandler) {
	r.mux.HandleFunc("/api/settings/preferences", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetPreferences(w, req)
		} else if req.Method == http.MethodPost {
			handler.UpdatePreferences(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/settings/export", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.ExportData(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/settings/import", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.ImportData(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/settings/reset", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.ResetSettings(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterTradingRoutes registers trading-related routes (API key config + bot control)
func (r *Router) RegisterTradingRoutes(handler *TradingHandler) {
	r.mux.HandleFunc("/api/trading/configure", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.ConfigureAPI(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/trading/start", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.StartBot(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/trading/stop", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.StopBot(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/trading/status", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetBotStatus(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/trading/portfolio", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetPortfolio(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterNewsRoutes registers news/sentiment/signals routes
func (r *Router) RegisterNewsRoutes(handler *NewsHandler) {
	r.mux.HandleFunc("/api/news", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetNews(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/sentiment/", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetSentiment(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/signals", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetSignals(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/market/sentiment", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetMarketSentiment(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterAuthRoutes registers authentication routes
func (r *Router) RegisterAuthRoutes(handler *AuthHandler) {
	r.mux.HandleFunc("/api/auth/login", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.Login(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/auth/register", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.Register(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/auth/config", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.Config(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/auth/logout", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.Logout(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/auth/me", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.Me(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterNotificationRoutes registers notification routes
func (r *Router) RegisterNotificationRoutes(handler *NotificationHandler) {
	r.mux.HandleFunc("/api/notifications", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetNotifications(w, req)
		case http.MethodDelete:
			handler.ClearAll(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/notifications/read-all", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPut {
			handler.MarkAllRead(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/notifications/", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPut:
			handler.MarkRead(w, req)
		case http.MethodDelete:
			handler.DeleteNotification(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterPerformanceRoutes registers performance analytics route
func (r *Router) RegisterPerformanceRoutes(handler *PerformanceHandler) {
	r.mux.HandleFunc("/api/performance", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetPerformance(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterJournalRoutes registers trading journal routes
func (r *Router) RegisterJournalRoutes(handler *JournalHandler) {
	r.mux.HandleFunc("/api/journal", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetEntries(w, req)
		case http.MethodPost:
			handler.CreateEntry(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/journal/", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPut:
			handler.UpdateEntry(w, req)
		case http.MethodDelete:
			handler.DeleteEntry(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterSLTPRoutes registers stop-loss/take-profit routes
func (r *Router) RegisterSLTPRoutes(handler *SLTPHandler) {
	r.mux.HandleFunc("/api/sltp", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.SetSLTP(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/sltp/", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetSLTP(w, req)
		case http.MethodDelete:
			handler.DeleteSLTP(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
}

// RegisterFinanceRoutes registers all finance-related routes
func (r *Router) RegisterFinanceRoutes(handler *FinanceHandler) {
	// Dashboard
	r.mux.HandleFunc("/api/finance/dashboard", handler.GetDashboard)

	// Net Worth
	r.mux.HandleFunc("/api/finance/net-worth/history", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetNetWorthHistory(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/net-worth/calculate", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodPost {
			handler.RecalculateNetWorth(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/net-worth", handler.GetNetWorth)

	// Accounts
	r.mux.HandleFunc("/api/finance/accounts", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetAccounts(w, req)
		case http.MethodPost:
			handler.CreateAccount(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/accounts/", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPut:
			handler.UpdateAccount(w, req)
		case http.MethodDelete:
			handler.DeleteAccount(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Transactions
	r.mux.HandleFunc("/api/finance/transactions", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetTransactions(w, req)
		case http.MethodPost:
			handler.CreateTransaction(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/transactions/", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPut:
			handler.UpdateTransaction(w, req)
		case http.MethodDelete:
			handler.DeleteTransaction(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Categories
	r.mux.HandleFunc("/api/finance/categories", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetCategories(w, req)
		case http.MethodPost:
			handler.CreateCategory(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Budgets
	r.mux.HandleFunc("/api/finance/budgets", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetBudgets(w, req)
		case http.MethodPost:
			handler.CreateBudget(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/budgets/", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPut:
			handler.UpdateBudget(w, req)
		case http.MethodDelete:
			handler.DeleteBudget(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Goals
	r.mux.HandleFunc("/api/finance/goals", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetGoals(w, req)
		case http.MethodPost:
			handler.CreateGoal(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/goals/", func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		if strings.Contains(path, "/add") && req.Method == http.MethodPost {
			handler.AddToGoal(w, req)
		} else {
			switch req.Method {
			case http.MethodPut:
				handler.UpdateGoal(w, req)
			case http.MethodDelete:
				handler.DeleteGoal(w, req)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		}
	})

	// Assets
	r.mux.HandleFunc("/api/finance/assets", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetAssets(w, req)
		case http.MethodPost:
			handler.CreateAsset(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/assets/", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPut:
			handler.UpdateAsset(w, req)
		case http.MethodDelete:
			handler.DeleteAsset(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Liabilities
	r.mux.HandleFunc("/api/finance/liabilities", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetLiabilities(w, req)
		case http.MethodPost:
			handler.CreateLiability(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/liabilities/", func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		if strings.Contains(path, "/payment") && req.Method == http.MethodPost {
			handler.MakePayment(w, req)
		} else {
			switch req.Method {
			case http.MethodPut:
				handler.UpdateLiability(w, req)
			case http.MethodDelete:
				handler.DeleteLiability(w, req)
			default:
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			}
		}
	})

	// Subscriptions
	r.mux.HandleFunc("/api/finance/subscriptions/upcoming", func(w http.ResponseWriter, req *http.Request) {
		if req.Method == http.MethodGet {
			handler.GetUpcomingBills(w, req)
		} else {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/subscriptions", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetSubscriptions(w, req)
		case http.MethodPost:
			handler.CreateSubscription(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/subscriptions/", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPut:
			handler.UpdateSubscription(w, req)
		case http.MethodDelete:
			handler.DeleteSubscription(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Financial Diary
	r.mux.HandleFunc("/api/finance/diary/date", handler.GetDiaryEntryByDate)
	r.mux.HandleFunc("/api/finance/diary", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodGet:
			handler.GetDiaryEntries(w, req)
		case http.MethodPost:
			handler.CreateDiaryEntry(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})
	r.mux.HandleFunc("/api/finance/diary/", func(w http.ResponseWriter, req *http.Request) {
		switch req.Method {
		case http.MethodPut:
			handler.UpdateDiaryEntry(w, req)
		case http.MethodDelete:
			handler.DeleteDiaryEntry(w, req)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
	})

	// Financial Calculators
	r.mux.HandleFunc("/api/finance/calculators/compound-interest", handler.CalculateCompoundInterest)
	r.mux.HandleFunc("/api/finance/calculators/loan", handler.CalculateLoan)
	r.mux.HandleFunc("/api/finance/calculators/savings-goal", handler.CalculateSavingsGoal)
	r.mux.HandleFunc("/api/finance/calculators/roi", handler.CalculateROI)
	r.mux.HandleFunc("/api/finance/calculators/asset-allocation", handler.CalculateAssetAllocation)
}

// Only the exact bootstrap and health endpoints bypass authentication.
func isPublicRoute(path string) bool {
	switch path {
	case "/api/auth/login", "/api/auth/register", "/api/auth/config", "/api/health", StrategyProxyPrefix + "/api/health":
		return true
	default:
		return false
	}
}

// rateLimiter implements a simple sliding window rate limiter with memory cap
type rateLimiter struct {
	mu      sync.Mutex
	clients map[string]*clientRate
	maxReqs int
	window  time.Duration
	maxKeys int // cap to prevent memory exhaustion
}

type clientRate struct {
	count   int
	resetAt time.Time
}

func newRateLimiter(maxReqs int, window time.Duration, maxKeys int) *rateLimiter {
	if maxKeys <= 0 {
		maxKeys = 10000 // default cap
	}
	return &rateLimiter{
		clients: make(map[string]*clientRate),
		maxReqs: maxReqs,
		window:  window,
		maxKeys: maxKeys,
	}
}

func (rl *rateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()

	// Periodic cleanup: remove expired entries
	if len(rl.clients) > rl.maxKeys {
		for k, v := range rl.clients {
			if now.After(v.resetAt) {
				delete(rl.clients, k)
			}
		}
	}

	// If still over cap after cleanup, reject new keys
	if len(rl.clients) >= rl.maxKeys {
		// Check if this key already exists
		if _, exists := rl.clients[key]; !exists {
			return false // Cap reached, reject unknown key
		}
	}

	client, exists := rl.clients[key]
	if !exists || now.After(client.resetAt) {
		rl.clients[key] = &clientRate{
			count:   1,
			resetAt: now.Add(rl.window),
		}
		return true
	}

	client.count++
	return client.count <= rl.maxReqs
}

// Global rate limiter: 1000 requests per minute per IP, max 10000 tracked IPs
// Increased from 100 to 1000 to support strategy container making concurrent
// requests for 10+ paper symbols + 5 real symbols + balance/ticker queries
var globalRateLimiter = newRateLimiter(1000, time.Minute, 10000)

// extractClientIP extracts the real client IP for the global rate limiter.
// Forwarding headers count only from TRUSTED_PROXIES; see clientIPFromRequest.
func extractClientIP(r *http.Request) string {
	return clientIPFromRequest(r)
}

// ServeHTTP implements http.Handler
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Rate limiting by real client IP
	clientIP := extractClientIP(req)
	if !globalRateLimiter.Allow(clientIP) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]string{
			"error": "Rate limit exceeded. Try again later.",
		})
		return
	}

	// Add CORS headers
	allowedOrigins := getEnv("FRONTEND_URL", "http://localhost:3000")
	origin := req.Header.Get("Origin")

	if origin != "" {
		// Validate origin against allowed list
		origins := strings.Split(allowedOrigins, ",")
		allowed := false
		for _, o := range origins {
			if strings.TrimSpace(o) == origin {
				allowed = true
				break
			}
		}
		if allowed {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		} else {
			// Reject preflight for disallowed origins
			if req.Method == http.MethodOptions {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			// Still serve but without CORS header
		}
	}

	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
	w.Header().Set("Access-Control-Allow-Credentials", "true")

	if req.Method == http.MethodOptions {
		w.WriteHeader(http.StatusOK)
		return
	}

	// Enforce the access matrix (route_access.go) on every route.
	rule := r.ruleFor(req)
	level := rule.levelFor(req.Method)
	if level != AccessPublic {
		token := extractBearerToken(req)
		if token == "" {
			writeAuthError(w, http.StatusUnauthorized, "Unauthorized")
			return
		}
		if rule.Service && r.isServiceToken(token) {
			r.mux.ServeHTTP(w, req)
			return
		}
		userID, ok := r.authHandler.ValidateToken(token)
		if !ok {
			writeAuthError(w, http.StatusUnauthorized, "Invalid or expired token")
			return
		}
		if level == AccessAdmin && !r.authHandler.IsAdmin(userID) {
			writeAuthError(w, http.StatusForbidden, "Admin role required")
			return
		}
		req = withUserID(req, userID)
	}

	r.mux.ServeHTTP(w, req)
}

func writeAuthError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// getEnv reads an environment variable with a fallback default
func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// Logger middleware for logging requests
func Logger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logger.Info("HTTP request", "method", r.Method, "path", r.URL.Path)
		next.ServeHTTP(w, r)
	})
}
