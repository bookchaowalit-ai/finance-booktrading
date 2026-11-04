package exchange

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"trading-bot-system/backend/internal/adapter/database"
	"trading-bot-system/backend/internal/adapter/exchange/bitkub"
	"trading-bot-system/backend/internal/config"
	"trading-bot-system/backend/internal/domain/model"
	"trading-bot-system/backend/internal/logger"
)

// ExchangeManager manages multiple exchange connections
type ExchangeManager struct {
	mu               sync.RWMutex
	currentProvider  config.ExchangeProvider
	binanceAdapter   *BinanceAdapter
	binanceExecutor  *BinanceOrderExecutor
	binanceTHAdapter *BinanceTHAdapter
	bitkubClient     *bitkub.Client
	apiKeys          map[string]*config.ExchangeAPIKey
	dbRepo           *database.APIKeyRepository
}

// Balance represents exchange balance
type Balance struct {
	Currency string  `json:"currency"`
	Free     float64 `json:"free"`
	Locked   float64 `json:"locked"`
	Total    float64 `json:"total"`
}

// TickerInfo represents ticker data
type TickerInfo struct {
	Symbol    string  `json:"symbol"`
	LastPrice float64 `json:"lastPrice"`
	HighPrice float64 `json:"highPrice,omitempty"`
	LowPrice  float64 `json:"lowPrice,omitempty"`
	Volume    float64 `json:"volume,omitempty"`
}

// ExchangeInfo represents exchange information
type ExchangeInfo struct {
	Provider  string    `json:"provider"`
	Name      string    `json:"name"`
	NameTH    string    `json:"name_th"`
	Connected bool      `json:"connected"`
	Testnet   bool      `json:"testnet"`
	Balances  []Balance `json:"balances,omitempty"`
}

// NewExchangeManager creates a new exchange manager
func NewExchangeManager(cfg *config.ExchangeConfig, dbRepo *database.APIKeyRepository) *ExchangeManager {
	manager := &ExchangeManager{
		currentProvider: cfg.Provider,
		apiKeys:         cfg.APIKeys,
		dbRepo:          dbRepo,
	}

	// Initialize adapters from env config keys first
	for provider, key := range cfg.APIKeys {
		if key.Enabled && key.APIKey != "" {
			switch provider {
			case string(config.ExchangeBinance):
				manager.binanceAdapter = NewBinanceAdapter(key.APIKey, key.APISecret, key.UseTestnet)
				manager.binanceExecutor = NewBinanceOrderExecutor(key.APIKey, key.APISecret, key.UseTestnet)
			case string(config.ExchangeBinanceTH):
				manager.binanceTHAdapter = NewBinanceTHAdapter(key.APIKey, key.APISecret)
			case string(config.ExchangeBitkub):
				manager.bitkubClient = bitkub.NewClient(key.APIKey, key.APISecret, key.UseTestnet)
			}
		}
	}

	// Load API keys from database and override config
	if dbRepo == nil {
		return manager
	}
	ctx := context.Background()
	dbKeys, err := dbRepo.GetAllAPIKeys(ctx)
	if err == nil && len(dbKeys) > 0 {
		for _, key := range dbKeys {
			manager.apiKeys[key.Provider] = &config.ExchangeAPIKey{
				APIKey:     key.APIKey,
				APISecret:  key.APISecret,
				UseTestnet: key.UseTestnet,
				Enabled:    true,
			}

			// Initialize adapters based on loaded keys (override env config)
			switch key.Provider {
			case string(config.ExchangeBinance):
				manager.binanceAdapter = NewBinanceAdapter(key.APIKey, key.APISecret, key.UseTestnet)
				manager.binanceExecutor = NewBinanceOrderExecutor(key.APIKey, key.APISecret, key.UseTestnet)
			case string(config.ExchangeBinanceTH):
				manager.binanceTHAdapter = NewBinanceTHAdapter(key.APIKey, key.APISecret)
			case string(config.ExchangeBitkub):
				manager.bitkubClient = bitkub.NewClient(key.APIKey, key.APISecret, key.UseTestnet)
			}
		}
	}

	return manager
}

// GetCurrentProvider returns the current exchange provider
func (m *ExchangeManager) GetCurrentProvider() config.ExchangeProvider {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.currentProvider
}

// SetCurrentProvider sets the current exchange provider
func (m *ExchangeManager) SetCurrentProvider(provider config.ExchangeProvider) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if we have credentials for this provider
	if _, ok := m.apiKeys[string(provider)]; !ok {
		return fmt.Errorf("no API credentials configured for provider: %s", provider)
	}

	m.currentProvider = provider
	return nil
}

// ConfigureExchange configures API keys for an exchange provider
func (m *ExchangeManager) ConfigureExchange(provider, apiKey, apiSecret string, useTestnet bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Save to database
	if m.dbRepo != nil {
		if err := m.dbRepo.SaveAPIKey(context.Background(), provider, apiKey, apiSecret, useTestnet); err != nil {
			return fmt.Errorf("failed to save to database: %w", err)
		}
	}

	// Create or update API key configuration in memory
	m.apiKeys[provider] = &config.ExchangeAPIKey{
		APIKey:     apiKey,
		APISecret:  apiSecret,
		UseTestnet: useTestnet,
		Enabled:    true,
	}

	// Re-initialize the appropriate adapter based on provider
	switch config.ExchangeProvider(provider) {
	case config.ExchangeBinance:
		m.binanceAdapter = NewBinanceAdapter(apiKey, apiSecret, useTestnet)
		m.binanceExecutor = NewBinanceOrderExecutor(apiKey, apiSecret, useTestnet)
	case config.ExchangeBinanceTH:
		m.binanceTHAdapter = NewBinanceTHAdapter(apiKey, apiSecret)
	case config.ExchangeBitkub:
		m.bitkubClient = bitkub.NewClient(apiKey, apiSecret, useTestnet)
	}

	return nil
}

// DeleteExchange deletes API keys for an exchange provider
func (m *ExchangeManager) DeleteExchange(provider string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Delete from database
	if m.dbRepo != nil {
		if err := m.dbRepo.DeleteAPIKey(context.Background(), provider); err != nil {
			// Don't fail if key doesn't exist in database
			logger.Info("Could not delete API key from database", "provider", provider, "error", err)
		}
	}

	// Remove from memory
	delete(m.apiKeys, provider)

	// Reset the appropriate adapter based on provider
	switch config.ExchangeProvider(provider) {
	case config.ExchangeBinance:
		m.binanceAdapter = nil
	case config.ExchangeBitkub:
		m.bitkubClient = nil
	}

	return nil
}

// StoredAPIKeyInfo represents masked API key info for display
type StoredAPIKeyInfo struct {
	ID        string `json:"id"`
	Exchange  string `json:"exchange"`
	APIKey    string `json:"apiKey"`
	APISecret string `json:"apiSecret"`
	Testnet   bool   `json:"testnet"`
	IsActive  bool   `json:"isActive"`
	CreatedAt string `json:"createdAt"`
}

// maskSecret returns first 4 chars + "****" for display
func maskSecret(s string) string {
	if len(s) <= 4 {
		return "****"
	}
	return s[:4] + "****"
}

// GetStoredAPIKeyInfos returns masked API key info from the database
func (m *ExchangeManager) GetStoredAPIKeyInfos(ctx context.Context) ([]StoredAPIKeyInfo, error) {
	if m.dbRepo == nil {
		return []StoredAPIKeyInfo{}, nil
	}
	keys, err := m.dbRepo.GetAllAPIKeys(ctx)
	if err != nil {
		return []StoredAPIKeyInfo{}, nil
	}
	result := make([]StoredAPIKeyInfo, 0, len(keys))
	for _, k := range keys {
		result = append(result, StoredAPIKeyInfo{
			ID:        k.Provider,
			Exchange:  k.Provider,
			APIKey:    maskSecret(k.APIKey),
			APISecret: maskSecret(k.APISecret),
			Testnet:   k.UseTestnet,
			IsActive:  true,
			CreatedAt: k.CreatedAt.Format("2006-01-02T15:04:05Z"),
		})
	}
	return result, nil
}

// GetSupportedExchanges returns list of supported exchanges with their status
func (m *ExchangeManager) GetSupportedExchanges() []ExchangeInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Check if Binance credentials exist
	binanceKey, hasBinance := m.apiKeys[string(config.ExchangeBinance)]
	binanceConnected := m.binanceAdapter != nil && hasBinance && binanceKey.Enabled
	binanceTestnet := false
	if hasBinance {
		binanceTestnet = binanceKey.UseTestnet
	}

	// Check if Binance TH credentials exist
	binanceTHKey, hasBinanceTH := m.apiKeys[string(config.ExchangeBinanceTH)]
	binanceTHConnected := m.binanceTHAdapter != nil && hasBinanceTH && binanceTHKey.Enabled
	binanceTHTestnet := false
	if hasBinanceTH {
		binanceTHTestnet = false // Binance TH doesn't have testnet
	}

	// Check if Bitkub credentials exist
	bitkubKey, hasBitkub := m.apiKeys[string(config.ExchangeBitkub)]
	bitkubConnected := m.bitkubClient != nil && hasBitkub && bitkubKey.Enabled
	bitkubTestnet := false
	if hasBitkub {
		bitkubTestnet = bitkubKey.UseTestnet
	}

	exchanges := []ExchangeInfo{
		{
			Provider:  string(config.ExchangeBinance),
			Name:      "Binance (Global)",
			NameTH:    "ไบแนนซ์ (ทั่วโลก)",
			Connected: binanceConnected,
			Testnet:   binanceTestnet,
		},
		{
			Provider:  string(config.ExchangeBinanceTH),
			Name:      "Binance TH (Thailand)",
			NameTH:    "ไบแนนซ์ ไทยแลนด์",
			Connected: binanceTHConnected,
			Testnet:   binanceTHTestnet,
		},
		{
			Provider:  string(config.ExchangeBitkub),
			Name:      "Bitkub",
			NameTH:    "บิทคับ",
			Connected: bitkubConnected,
			Testnet:   bitkubTestnet,
		},
	}

	return exchanges
}

// GetBalances returns balances for the current exchange
func (m *ExchangeManager) GetBalances(ctx context.Context) ([]Balance, error) {
	m.mu.RLock()
	provider := m.currentProvider
	m.mu.RUnlock()

	switch provider {
	case config.ExchangeBinance:
		return m.getBinanceBalances(ctx)
	case config.ExchangeBinanceTH:
		return m.getBinanceTHBalances(ctx)
	case config.ExchangeBitkub:
		return m.getBitkubBalances()
	default:
		return nil, fmt.Errorf("unsupported exchange: %s", provider)
	}
}

// PlaceOrder places an order on the current exchange
func (m *ExchangeManager) PlaceOrder(ctx context.Context, symbol string, side string, quantity float64, price float64) (interface{}, error) {
	return m.PlaceOrderOn(ctx, m.GetCurrentProvider(), symbol, side, quantity, price)
}

// PlaceOrderOn places an order on the given provider, so a caller can record
// the provider it trades on before submitting and never race a provider switch.
func (m *ExchangeManager) PlaceOrderOn(ctx context.Context, provider config.ExchangeProvider, symbol string, side string, quantity float64, price float64) (interface{}, error) {
	switch provider {
	case config.ExchangeBinance:
		return m.placeBinanceOrder(ctx, symbol, side, quantity, price)
	case config.ExchangeBinanceTH:
		return m.placeBinanceTHOrder(ctx, symbol, side, quantity, price)
	case config.ExchangeBitkub:
		return m.placeBitkubOrder(symbol, side, quantity, price)
	default:
		return nil, fmt.Errorf("unsupported exchange: %s", provider)
	}
}

// PlaceOrderWithClientID places an order tagged with clientOrderID and
// returns its exchange-neutral state. An error wrapping ErrOrderStateUnknown
// (see IsOrderStateUnknown) means the order may exist: look it up with
// LookupOrderByClientID before placing anything else.
func (m *ExchangeManager) PlaceOrderWithClientID(ctx context.Context, symbol, side string, quantity, price float64, clientOrderID string) (*OrderReport, error) {
	return m.PlaceOrderWithClientIDOn(ctx, m.GetCurrentProvider(), symbol, side, quantity, price, clientOrderID)
}

// PlaceOrderWithClientIDOn is PlaceOrderWithClientID on the given provider.
func (m *ExchangeManager) PlaceOrderWithClientIDOn(ctx context.Context, provider config.ExchangeProvider, symbol, side string, quantity, price float64, clientOrderID string) (*OrderReport, error) {
	if !ValidClientOrderID(clientOrderID) {
		return nil, fmt.Errorf("invalid client order ID %q", clientOrderID)
	}
	m.mu.RLock()
	executor := m.binanceExecutor
	thAdapter := m.binanceTHAdapter
	bk := m.bitkubClient
	m.mu.RUnlock()

	switch provider {
	case config.ExchangeBinance:
		if executor == nil {
			return nil, fmt.Errorf("Binance order executor not initialized — configure API keys first")
		}
		orderType := model.OrderTypeMarket
		if price > 0 {
			orderType = model.OrderTypeLimit
		}
		placed, err := executor.PlaceOrder(ctx, &model.Order{
			ID:       clientOrderID,
			Symbol:   model.TradeSymbol(symbol),
			Side:     model.OrderSide(side),
			Type:     orderType,
			Quantity: quantity,
			Price:    price,
		})
		if err != nil {
			return nil, err
		}
		status := "NEW"
		switch placed.Status {
		case model.OrderStatusFilled:
			status = "FILLED"
		case model.OrderStatusRejected:
			status = "REJECTED"
		}
		return &OrderReport{ClientOrderID: clientOrderID, Status: status, ExecutedQty: placed.Quantity}, nil
	case config.ExchangeBinanceTH:
		if thAdapter == nil {
			return nil, fmt.Errorf("Binance TH adapter not initialized")
		}
		orderType, timeInForce := "MARKET", ""
		if price > 0 {
			orderType, timeInForce = "LIMIT", "GTC"
		}
		placed, err := thAdapter.PlaceOrderWithClientID(ctx, symbol, side, orderType, quantity, price, timeInForce, clientOrderID)
		if err != nil {
			return nil, err
		}
		return reportFromOrder(placed), nil
	case config.ExchangeBitkub:
		if bk == nil {
			return nil, fmt.Errorf("Bitkub client not initialized")
		}
		return placeBitkubWithClientID(ctx, bk, symbol, side, quantity, price, clientOrderID)
	default:
		return nil, fmt.Errorf("%w: %s", ErrReconcileUnsupported, provider)
	}
}

// LookupOrderByClientID returns the current state of an order placed with
// PlaceOrderWithClientID, or ErrOrderNotFound when the exchange never
// accepted it.
func (m *ExchangeManager) LookupOrderByClientID(ctx context.Context, symbol, clientOrderID string) (*OrderReport, error) {
	return m.LookupOrderByClientIDSince(ctx, symbol, clientOrderID, time.Time{})
}

// LookupOrderByClientIDSince is LookupOrderByClientID for an order submitted
// at or after since. Binance ignores since. Bitkub cannot query by client
// order ID, so it scans open orders and the fills made since then (see
// bitkub.FindOrderByClientID); a zero since makes absence unprovable there.
func (m *ExchangeManager) LookupOrderByClientIDSince(ctx context.Context, symbol, clientOrderID string, since time.Time) (*OrderReport, error) {
	return m.LookupOrderByClientIDOn(ctx, m.GetCurrentProvider(), symbol, clientOrderID, since)
}

// LookupOrderByClientIDOn is LookupOrderByClientIDSince on the given provider:
// an order is looked up where it was placed, whatever the current provider.
func (m *ExchangeManager) LookupOrderByClientIDOn(ctx context.Context, provider config.ExchangeProvider, symbol, clientOrderID string, since time.Time) (*OrderReport, error) {
	m.mu.RLock()
	executor := m.binanceExecutor
	thAdapter := m.binanceTHAdapter
	bk := m.bitkubClient
	m.mu.RUnlock()

	if provider == config.ExchangeBitkub && bk != nil {
		return lookupBitkubByClientID(ctx, bk, symbol, clientOrderID, since)
	}

	var (
		order *Order
		err   error
	)
	switch {
	case provider == config.ExchangeBinance && executor != nil:
		order, err = executor.GetOrderByClientID(ctx, symbol, clientOrderID)
	case provider == config.ExchangeBinanceTH && thAdapter != nil:
		order, err = thAdapter.GetOrderByClientID(ctx, symbol, clientOrderID)
	default:
		return nil, fmt.Errorf("%w: %s", ErrReconcileUnsupported, provider)
	}
	if err != nil {
		return nil, err
	}
	return reportFromOrder(order), nil
}

// CancelOrderByClientID cancels an open order placed with
// PlaceOrderWithClientID. ErrOrderNotFound means it is not open any more;
// look it up to learn its final state. since is the submission time, used
// by Bitkub to find the order (see LookupOrderByClientIDSince).
func (m *ExchangeManager) CancelOrderByClientID(ctx context.Context, symbol, clientOrderID string, since time.Time) error {
	return m.CancelOrderByClientIDOn(ctx, m.GetCurrentProvider(), symbol, clientOrderID, since)
}

// CancelOrderByClientIDOn is CancelOrderByClientID on the given provider.
func (m *ExchangeManager) CancelOrderByClientIDOn(ctx context.Context, provider config.ExchangeProvider, symbol, clientOrderID string, since time.Time) error {
	m.mu.RLock()
	executor := m.binanceExecutor
	thAdapter := m.binanceTHAdapter
	bk := m.bitkubClient
	m.mu.RUnlock()

	switch {
	case provider == config.ExchangeBinance && executor != nil:
		return executor.CancelOrderByClientID(ctx, symbol, clientOrderID)
	case provider == config.ExchangeBinanceTH && thAdapter != nil:
		return thAdapter.CancelOrderByClientID(ctx, symbol, clientOrderID)
	case provider == config.ExchangeBitkub && bk != nil:
		if since.IsZero() {
			return fmt.Errorf("Bitkub cancel of %s needs the submission time", clientOrderID)
		}
		return cancelBitkubByClientID(ctx, bk, symbol, clientOrderID, since)
	default:
		return fmt.Errorf("%w: %s", ErrReconcileUnsupported, provider)
	}
}

// GetTicker returns ticker info for a symbol from the current exchange
func (m *ExchangeManager) GetTicker(ctx context.Context, symbol string) (*TickerInfo, error) {
	return m.GetTickerOn(ctx, m.GetCurrentProvider(), symbol)
}

// MarketSymbol is a plain market name (BTCTHB, Bitkub's THB_BTC): symbols
// are put into exchange URLs unescaped.
var MarketSymbol = regexp.MustCompile(`^[A-Za-z0-9_]{2,30}$`)

// ListedMarket accepts a ticker only when it names the requested market and
// has a price: Binance Global parses an "Invalid symbol" error body into an
// empty ticker with no error.
func ListedMarket(symbol string, ticker *TickerInfo, err error) error {
	if err != nil {
		return err
	}
	if ticker == nil || !strings.EqualFold(ticker.Symbol, symbol) || ticker.LastPrice <= 0 {
		return fmt.Errorf("no ticker for %s", symbol)
	}
	return nil
}

// MarketListed reports whether the named exchange lists symbol.
func (m *ExchangeManager) MarketListed(ctx context.Context, provider config.ExchangeProvider, symbol string) error {
	if !MarketSymbol.MatchString(symbol) {
		return fmt.Errorf("malformed symbol %q", symbol)
	}
	ticker, err := m.GetTickerOn(ctx, provider, symbol)
	return ListedMarket(symbol, ticker, err)
}

// GetTickerOn returns ticker info for a symbol from the named exchange.
func (m *ExchangeManager) GetTickerOn(ctx context.Context, provider config.ExchangeProvider, symbol string) (*TickerInfo, error) {
	switch provider {
	case config.ExchangeBinance:
		return m.getBinanceTicker(ctx, symbol)
	case config.ExchangeBinanceTH:
		return m.getBinanceTHTicker(ctx, symbol)
	case config.ExchangeBitkub:
		return m.getBitkubTicker(symbol)
	default:
		return nil, fmt.Errorf("unsupported exchange: %s", provider)
	}
}

// GetBinanceTHBalances gets balances from Binance Thailand
func (m *ExchangeManager) getBinanceTHBalances(ctx context.Context) ([]Balance, error) {
	if m.binanceTHAdapter == nil {
		return nil, fmt.Errorf("Binance TH adapter not initialized")
	}
	return m.binanceTHAdapter.GetBalances(ctx)
}

// GetBinanceTHTicker gets ticker from Binance Thailand
func (m *ExchangeManager) getBinanceTHTicker(ctx context.Context, symbol string) (*TickerInfo, error) {
	if m.binanceTHAdapter == nil {
		// Binance TH's public ticker endpoint does not require credentials.
		// Keep private account/order operations disabled until API keys exist.
		return NewBinanceTHAdapter("", "").GetTicker(ctx, symbol)
	}
	return m.binanceTHAdapter.GetTicker(ctx, symbol)
}

// GetBinanceBalances gets balances from Binance
func (m *ExchangeManager) getBinanceBalances(ctx context.Context) ([]Balance, error) {
	if m.binanceAdapter == nil {
		return nil, fmt.Errorf("Binance adapter not initialized")
	}
	return m.binanceAdapter.GetBalances(ctx)
}

// GetBinanceTicker gets ticker from Binance
func (m *ExchangeManager) getBinanceTicker(ctx context.Context, symbol string) (*TickerInfo, error) {
	if m.binanceAdapter == nil {
		return nil, fmt.Errorf("Binance adapter not initialized")
	}
	return m.binanceAdapter.GetTicker(ctx, symbol)
}

// GetBitkubBalances gets balances from Bitkub
func (m *ExchangeManager) getBitkubBalances() ([]Balance, error) {
	if m.bitkubClient == nil {
		return nil, fmt.Errorf("Bitkub client not initialized")
	}

	// Get balances from Bitkub API
	balances, err := m.bitkubClient.GetBalances()
	if err != nil {
		return nil, fmt.Errorf("failed to get Bitkub balances: %w", err)
	}

	result := make([]Balance, 0, len(balances))
	for _, b := range balances {
		result = append(result, Balance{
			Currency: b.Currency,
			Free:     b.Available,
			Locked:   b.Amount - b.Available,
			Total:    b.Amount,
		})
	}

	return result, nil
}

// GetBitkubTicker gets ticker from Bitkub
func (m *ExchangeManager) getBitkubTicker(symbol string) (*TickerInfo, error) {
	if m.bitkubClient == nil {
		return nil, fmt.Errorf("Bitkub client not initialized")
	}
	ticker, err := m.bitkubClient.GetTicker(symbol)
	if err != nil {
		return nil, err
	}
	return &TickerInfo{
		Symbol:    symbol,
		LastPrice: ticker.LastPrice,
		HighPrice: ticker.High24h,
		LowPrice:  ticker.Low24h,
		Volume:    ticker.Volume24h,
	}, nil
}

// placeBinanceOrder places a REAL order on Binance Global via the order executor
func (m *ExchangeManager) placeBinanceOrder(ctx context.Context, symbol string, side string, quantity float64, price float64) (interface{}, error) {
	if m.binanceExecutor == nil {
		return nil, fmt.Errorf("Binance order executor not initialized — configure API keys first")
	}

	// Determine order type
	orderType := model.OrderTypeMarket
	if price > 0 {
		orderType = model.OrderTypeLimit
	}

	order := &model.Order{
		ID:       fmt.Sprintf("real_%d", time.Now().UnixNano()),
		Symbol:   model.TradeSymbol(symbol),
		Side:     model.OrderSide(side),
		Type:     orderType,
		Quantity: quantity,
		Price:    price,
	}

	result, err := m.binanceExecutor.PlaceOrder(ctx, order)
	if err != nil {
		return nil, fmt.Errorf("Binance order failed: %w", err)
	}

	return result, nil
}

// placeBinanceTHOrder places an order on Binance Thailand
func (m *ExchangeManager) placeBinanceTHOrder(ctx context.Context, symbol string, side string, quantity float64, price float64) (interface{}, error) {
	if m.binanceTHAdapter == nil {
		return nil, fmt.Errorf("Binance TH adapter not initialized")
	}

	// Determine order type
	orderType := "MARKET"
	timeInForce := ""

	if price > 0 {
		orderType = "LIMIT"
		timeInForce = "GTC" // Good Til Canceled
	}

	return m.binanceTHAdapter.PlaceOrder(ctx, symbol, side, orderType, quantity, price, timeInForce)
}

// GetOpenOrders gets all open orders for the current exchange
func (m *ExchangeManager) GetOpenOrders(ctx context.Context, symbol string) (interface{}, error) {
	return m.GetOpenOrdersOn(ctx, m.GetCurrentProvider(), symbol)
}

// GetOpenOrdersOn is GetOpenOrders on the given provider.
func (m *ExchangeManager) GetOpenOrdersOn(ctx context.Context, provider config.ExchangeProvider, symbol string) (interface{}, error) {

	switch provider {
	case config.ExchangeBinance:
		return nil, fmt.Errorf("Binance open orders not yet implemented")
	case config.ExchangeBinanceTH:
		if m.binanceTHAdapter == nil {
			return nil, fmt.Errorf("Binance TH adapter not initialized")
		}
		return m.binanceTHAdapter.GetOpenOrders(ctx, symbol)
	case config.ExchangeBitkub:
		return nil, fmt.Errorf("Bitkub open orders not yet implemented")
	default:
		return nil, fmt.Errorf("unsupported exchange: %s", provider)
	}
}

// CancelOrder cancels an order on the current exchange
func (m *ExchangeManager) CancelOrder(ctx context.Context, symbol string, orderID int64) error {
	return m.CancelOrderOn(ctx, m.GetCurrentProvider(), symbol, orderID)
}

// CancelOrderOn is CancelOrder on the given provider.
func (m *ExchangeManager) CancelOrderOn(ctx context.Context, provider config.ExchangeProvider, symbol string, orderID int64) error {

	switch provider {
	case config.ExchangeBinanceTH:
		if m.binanceTHAdapter == nil {
			return fmt.Errorf("Binance TH adapter not initialized")
		}
		return m.binanceTHAdapter.CancelOrder(ctx, symbol, orderID)
	default:
		return fmt.Errorf("cancel order not supported for exchange: %s", provider)
	}
}

// GetOrderStatus queries a single order's current status on the current exchange
func (m *ExchangeManager) GetOrderStatus(ctx context.Context, symbol string, orderID int64) (*Order, error) {
	return m.GetOrderStatusOn(ctx, m.GetCurrentProvider(), symbol, orderID)
}

// GetOrderStatusOn is GetOrderStatus on the given provider.
func (m *ExchangeManager) GetOrderStatusOn(ctx context.Context, provider config.ExchangeProvider, symbol string, orderID int64) (*Order, error) {

	switch provider {
	case config.ExchangeBinanceTH:
		if m.binanceTHAdapter == nil {
			return nil, fmt.Errorf("Binance TH adapter not initialized")
		}
		return m.binanceTHAdapter.GetOrderStatus(ctx, symbol, orderID)
	default:
		return nil, fmt.Errorf("order status not supported for exchange: %s", provider)
	}
}

// placeBitkubOrder places an order on Bitkub
func (m *ExchangeManager) placeBitkubOrder(symbol string, side string, quantity float64, price float64) (interface{}, error) {
	if m.bitkubClient == nil {
		return nil, fmt.Errorf("Bitkub client not initialized")
	}
	return m.bitkubClient.PlaceOrder(symbol, side, "LIMIT", quantity, price)
}

// Connect connects to the current exchange WebSocket
func (m *ExchangeManager) Connect(ctx context.Context) error {
	m.mu.RLock()
	provider := m.currentProvider
	adapter := m.binanceAdapter
	m.mu.RUnlock()

	if provider == config.ExchangeBinance && adapter != nil {
		return adapter.Connect(ctx)
	}

	return nil
}

// Disconnect disconnects from the current exchange WebSocket
func (m *ExchangeManager) Disconnect(ctx context.Context) error {
	m.mu.RLock()
	provider := m.currentProvider
	adapter := m.binanceAdapter
	m.mu.RUnlock()

	if provider == config.ExchangeBinance && adapter != nil {
		return adapter.Disconnect(ctx)
	}

	return nil
}
