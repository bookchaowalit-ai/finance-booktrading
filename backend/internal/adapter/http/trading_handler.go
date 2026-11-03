package http

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"trading-bot-system/backend/internal/domain/service"
	"trading-bot-system/backend/internal/port/input"
)

// TradingHandler handles trading-related HTTP requests
type TradingHandler struct {
	tradingService *service.TradingService
	botService     input.BotHandler
}

// NewTradingHandler creates a new trading handler
func NewTradingHandler(tradingService *service.TradingService, botService input.BotHandler) *TradingHandler {
	return &TradingHandler{
		tradingService: tradingService,
		botService:     botService,
	}
}

// ConfigureRequest represents API key configuration request
type ConfigureRequest struct {
	APIKey    string `json:"apiKey"`
	APISecret string `json:"apiSecret"`
	Testnet   bool   `json:"testnet"`
}

// StartBotRequest represents bot start request
type StartBotRequest struct {
	Symbol     string  `json:"symbol"`
	Quantity   float64 `json:"quantity"`
	GridLevels int     `json:"gridLevels"`
	LowerPrice float64 `json:"lowerPrice"`
	UpperPrice float64 `json:"upperPrice"`
	Investment float64 `json:"investment"`
}

// StartBot starts the trading bot (delegates to BotService)
func (h *TradingHandler) StartBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req StartBotRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request body"})
		return
	}

	// Validate input
	if err := req.Validate(); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Delegate to BotService
	params := &input.BotStartParams{
		Symbol:     req.Symbol,
		Quantity:   req.Quantity,
		GridLevels: req.GridLevels,
		LowerPrice: req.LowerPrice,
		UpperPrice: req.UpperPrice,
		Investment: req.Investment,
	}

	err := h.botService.Start(r.Context(), params)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "started",
	})
}

// Validate checks that all fields have valid values
func (r *StartBotRequest) Validate() error {
	if r.Symbol == "" {
		return fmt.Errorf("symbol is required")
	}
	if r.Quantity <= 0 {
		return fmt.Errorf("quantity must be greater than 0")
	}
	if r.GridLevels < 0 {
		return fmt.Errorf("gridLevels must be 0 or greater")
	}
	if r.LowerPrice <= 0 {
		return fmt.Errorf("lowerPrice must be greater than 0")
	}
	if r.UpperPrice <= 0 {
		return fmt.Errorf("upperPrice must be greater than 0")
	}
	if r.LowerPrice >= r.UpperPrice {
		return fmt.Errorf("lowerPrice (%.2f) must be less than upperPrice (%.2f)", r.LowerPrice, r.UpperPrice)
	}
	if r.Investment <= 0 {
		return fmt.Errorf("investment must be greater than 0")
	}
	return nil
}

// StopBot stops the trading bot (delegates to BotService)
func (h *TradingHandler) StopBot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	err := h.botService.Stop(r.Context())
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, input.ErrBotNotRunning) {
			code = http.StatusConflict
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "stopped",
	})
}

// GetBotStatus returns the bot status (delegates to BotService)
func (h *TradingHandler) GetBotStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	status, err := h.botService.GetStatus(r.Context())
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// GetPortfolio returns the user's portfolio (uses TradingService)
func (h *TradingHandler) GetPortfolio(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	portfolio, err := h.tradingService.GetPortfolio()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{
			"error": err.Error(),
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(portfolio)
}

// ConfigureAPI configures API keys (called from frontend)
func (h *TradingHandler) ConfigureAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ConfigureRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "Invalid request body"})
		return
	}

	if req.APIKey == "" || req.APISecret == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "apiKey and apiSecret are required"})
		return
	}

	// Configure the trading service with real API keys
	h.tradingService.ConfigureClient(req.APIKey, req.APISecret, req.Testnet)

	// Also set the client on BotService so it can do grid trading
	if botSvc, ok := h.botService.(*service.BotServiceImpl); ok {
		botSvc.SetTradingClient(h.tradingService.GetClient())
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status": "configured",
	})
}
