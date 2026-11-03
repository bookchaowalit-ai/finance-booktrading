package service

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"sync"
	"time"

	"trading-bot-system/backend/internal/adapter/exchange"
	"trading-bot-system/backend/internal/adapter/exchange/bitkub"
	"trading-bot-system/backend/internal/domain/model"
	"trading-bot-system/backend/internal/domain/repository"
	"trading-bot-system/backend/internal/logger"
	"trading-bot-system/backend/internal/port/input"
	"trading-bot-system/backend/internal/port/output"

	"github.com/google/uuid"
)

// OrderServiceImpl implements the OrderService interface
type OrderServiceImpl struct {
	orderRepo        repository.OrderRepository
	tradeHistoryRepo repository.TradeHistoryRepository
	orderExecutor    output.OrderExecutor
	broadcaster      output.WebSocketBroadcaster
	mu               sync.Mutex
}

// NewOrderService creates a new order service
func NewOrderService(
	orderRepo repository.OrderRepository,
	tradeHistoryRepo repository.TradeHistoryRepository,
	orderExecutor output.OrderExecutor,
	broadcaster output.WebSocketBroadcaster,
) input.OrderHandler {
	return &OrderServiceImpl{
		orderRepo:        orderRepo,
		tradeHistoryRepo: tradeHistoryRepo,
		orderExecutor:    orderExecutor,
		broadcaster:      broadcaster,
	}
}

// CreateOrder creates a new order
func (s *OrderServiceImpl) CreateOrder(ctx context.Context, req *model.OrderRequest) (*model.OrderResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	order := &model.Order{
		ID:        uuid.New().String(),
		Symbol:    req.Symbol,
		Side:      req.Side,
		Type:      model.OrderTypeMarket,
		Quantity:  req.Quantity,
		Price:     req.Price,
		Status:    model.OrderStatusPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Save order to repository
	if err := s.orderRepo.Create(ctx, order); err != nil {
		return nil, fmt.Errorf("failed to create order: %w", err)
	}

	// Execute order on exchange
	executedOrder, err := s.orderExecutor.PlaceOrder(ctx, order)
	if err != nil {
		order.Status = model.OrderStatusRejected
		s.orderRepo.UpdateStatus(ctx, order.ID, model.OrderStatusRejected)
		return nil, fmt.Errorf("failed to execute order: %w", err)
	}

	// Update order status
	executedOrder.UpdatedAt = time.Now()
	if err := s.orderRepo.UpdateStatus(ctx, executedOrder.ID, executedOrder.Status); err != nil {
		logger.Info("Failed to update order status", "error", err)
	}

	// Create trade history if filled
	if executedOrder.Status == model.OrderStatusFilled {
		trade := &model.TradeHistory{
			ID:         uuid.New().String(),
			Symbol:     executedOrder.Symbol,
			Side:       executedOrder.Side,
			Quantity:   executedOrder.Quantity,
			Price:      executedOrder.Price,
			Total:      executedOrder.Quantity * executedOrder.Price,
			Fee:        executedOrder.Quantity * executedOrder.Price * 0.001, // 0.1% fee
			ExecutedAt: time.Now(),
		}
		s.tradeHistoryRepo.Add(ctx, trade)
	}

	// Broadcast order update
	s.broadcaster.BroadcastOrderUpdate(executedOrder)

	return &model.OrderResponse{
		OrderID:   executedOrder.ID,
		Symbol:    executedOrder.Symbol,
		Side:      executedOrder.Side,
		Quantity:  executedOrder.Quantity,
		Status:    executedOrder.Status,
		CreatedAt: executedOrder.CreatedAt,
	}, nil
}

// CancelOrder cancels an existing order
func (s *OrderServiceImpl) CancelOrder(ctx context.Context, orderID string) error {
	order, err := s.orderRepo.GetByID(ctx, orderID)
	if err != nil {
		return fmt.Errorf("failed to get order: %w", err)
	}

	if order.Status != model.OrderStatusPending {
		return fmt.Errorf("cannot cancel order with status %s", order.Status)
	}

	if err := s.orderExecutor.CancelOrder(ctx, orderID, order.Symbol); err != nil {
		return fmt.Errorf("failed to cancel order on exchange: %w", err)
	}

	if err := s.orderRepo.UpdateStatus(ctx, orderID, model.OrderStatusCancelled); err != nil {
		return fmt.Errorf("failed to update order status: %w", err)
	}

	// Broadcast order update
	s.broadcaster.BroadcastOrderUpdate(&model.Order{
		ID:     orderID,
		Status: model.OrderStatusCancelled,
	})

	return nil
}

// GetOrder retrieves an order by ID
func (s *OrderServiceImpl) GetOrder(ctx context.Context, orderID string) (*model.Order, error) {
	return s.orderRepo.GetByID(ctx, orderID)
}

// GetAllOrders retrieves all orders
func (s *OrderServiceImpl) GetAllOrders(ctx context.Context) ([]*model.Order, error) {
	return s.orderRepo.GetAll(ctx)
}

// GetOpenOrders retrieves all orders with PENDING status
func (s *OrderServiceImpl) GetOpenOrders(ctx context.Context) ([]*model.Order, error) {
	orders, err := s.orderRepo.GetAll(ctx)
	if err != nil {
		return nil, err
	}
	open := make([]*model.Order, 0)
	for _, o := range orders {
		if o.Status == model.OrderStatusPending {
			open = append(open, o)
		}
	}
	return open, nil
}

// MarketDataServiceImpl implements the MarketDataService interface
type MarketDataServiceImpl struct {
	exchangeStream output.ExchangeDataStream
	marketDataRepo repository.MarketDataRepository
	publisher      output.MarketDataPublisher
	broadcaster    output.WebSocketBroadcaster
	streaming      map[model.TradeSymbol]bool
	streamingMu    sync.RWMutex
	ctx            context.Context
	cancel         context.CancelFunc
}

// NewMarketDataService creates a new market data service
func NewMarketDataService(
	exchangeStream output.ExchangeDataStream,
	marketDataRepo repository.MarketDataRepository,
	publisher output.MarketDataPublisher,
	broadcaster output.WebSocketBroadcaster,
) input.MarketDataHandler {
	ctx, cancel := context.WithCancel(context.Background())
	return &MarketDataServiceImpl{
		exchangeStream: exchangeStream,
		marketDataRepo: marketDataRepo,
		publisher:      publisher,
		broadcaster:    broadcaster,
		streaming:      make(map[model.TradeSymbol]bool),
		ctx:            ctx,
		cancel:         cancel,
	}
}

// GetLatestPrice retrieves the latest price for a symbol
func (s *MarketDataServiceImpl) GetLatestPrice(ctx context.Context, symbol model.TradeSymbol) (*model.MarketData, error) {
	return s.marketDataRepo.GetLatest(ctx, symbol)
}

// GetPriceHistory retrieves price history for a symbol
func (s *MarketDataServiceImpl) GetPriceHistory(ctx context.Context, symbol model.TradeSymbol) ([]*model.MarketData, error) {
	return s.marketDataRepo.GetPriceHistory(ctx, symbol, 24*time.Hour)
}

// Subscribe subscribes to market data for a symbol
func (s *MarketDataServiceImpl) Subscribe(ctx context.Context, symbol model.TradeSymbol) (<-chan *model.MarketData, error) {
	// Simple implementation - return the exchange stream channel
	// In production, implement proper subscription management
	ch := make(chan *model.MarketData, 100)

	// Start forwarding messages
	go func() {
		streamCh := s.exchangeStream.GetStreamChannel()
		for {
			select {
			case <-ctx.Done():
				close(ch)
				return
			case data, ok := <-streamCh:
				if !ok {
					close(ch)
					return
				}
				if data.Symbol == symbol {
					select {
					case ch <- data:
					default:
						// Channel full, skip
					}
				}
			}
		}
	}()

	return ch, nil
}

// Unsubscribe unsubscribes from market data for a symbol
func (s *MarketDataServiceImpl) Unsubscribe(ctx context.Context, symbol model.TradeSymbol) error {
	// Simple implementation - just log
	logger.Info("Unsubscribed from market data", "symbol", symbol)
	return nil
}

// StartStreaming starts streaming market data for a symbol
func (s *MarketDataServiceImpl) StartStreaming(ctx context.Context, symbol model.TradeSymbol) error {
	s.streamingMu.Lock()
	defer s.streamingMu.Unlock()

	if s.streaming[symbol] {
		return nil
	}

	if err := s.exchangeStream.Subscribe(ctx, symbol); err != nil {
		return fmt.Errorf("failed to subscribe to exchange: %w", err)
	}

	s.streaming[symbol] = true

	// Start processing stream
	go s.processStream(symbol)

	logger.Info("Started streaming market data", "symbol", symbol)
	return nil
}

// StopStreaming stops streaming market data for a symbol
func (s *MarketDataServiceImpl) StopStreaming(ctx context.Context, symbol model.TradeSymbol) error {
	s.streamingMu.Lock()
	defer s.streamingMu.Unlock()

	if !s.streaming[symbol] {
		return nil
	}

	if err := s.exchangeStream.Unsubscribe(ctx, symbol); err != nil {
		return fmt.Errorf("failed to unsubscribe from exchange: %w", err)
	}

	s.streaming[symbol] = false

	logger.Info("Stopped streaming market data", "symbol", symbol)
	return nil
}

func (s *MarketDataServiceImpl) processStream(symbol model.TradeSymbol) {
	streamCh := s.exchangeStream.GetStreamChannel()

	for {
		select {
		case <-s.ctx.Done():
			return
		case data, ok := <-streamCh:
			if !ok {
				return
			}

			// Filter by symbol if needed
			if data.Symbol != symbol {
				continue
			}

			// Save to cache
			s.marketDataRepo.Save(s.ctx, data)

			// Publish to Redis
			if err := s.publisher.PublishMarketData(s.ctx, data); err != nil {
				logger.Info("Failed to publish market data", "error", err)
			}

			// Broadcast to WebSocket clients
			s.broadcaster.BroadcastMarketData(data)
		}
	}
}

// Shutdown stops all streaming
func (s *MarketDataServiceImpl) Shutdown() {
	s.cancel()
}

// BotServiceImpl implements the BotService interface
type BotServiceImpl struct {
	botStatusRepo   repository.BotStatusRepository
	orderSignalSub  output.RedisPublisher
	broadcaster     output.WebSocketBroadcaster
	tradingClient   *bitkub.Client
	exchangeManager *exchange.ExchangeManager // pointer so nil comparison works
	isRunning       bool
	runningMu       sync.RWMutex
	// runLoops tracks the trading goroutines of the current run (a fresh
	// WaitGroup per Start). Stop waits on runJoined, closed once that run's
	// loops exit; stopPending stays set until a Stop has seen them exit, so a
	// retried Stop never reports "not running" while a loop is still alive.
	runLoops      *sync.WaitGroup
	tradedMu      sync.Mutex
	tradedSymbols map[string]bool
	// unresolved holds submissions a stopped loop left with an unknown
	// outcome, keyed by client order ID; Stop looks them up (see
	// reconcileUnresolved). orderLookup builds the exchange view for that.
	unresolved     map[string]unresolvedOrder
	orderLookup    func(symbol string) gridExchange
	runJoined      chan struct{}
	stopPending    bool
	stopFinalizing bool
	startedAt      time.Time
	ctx            context.Context
	cancel         context.CancelFunc
	// Grid trading fields
	symbol      string
	quantity    float64
	gridLevels  int
	lowerPrice  float64
	upperPrice  float64
	investment  float64
	botStatus   *model.BotStatus
	tradesCount int
	totalProfit float64
	botMode     model.BotMode // Current operating mode
	// Signal / Auto mode fields
	signalConfig input.SignalConfig
	positions    map[string]*positionInfo // symbol -> position tracking
}

type positionInfo struct {
	entryPrice  float64
	quantity    float64
	entryTime   time.Time
	safetyCount int
}

// NewBotService creates a new bot service
func NewBotService(
	botStatusRepo repository.BotStatusRepository,
	orderSignalSub output.RedisPublisher,
	broadcaster output.WebSocketBroadcaster,
) *BotServiceImpl {
	return &BotServiceImpl{
		botStatusRepo:  botStatusRepo,
		orderSignalSub: orderSignalSub,
		broadcaster:    broadcaster,
		botStatus: &model.BotStatus{
			IsActive:    false,
			TotalTrades: 0,
			TotalProfit: 0,
		},
	}
}

// SetTradingClient sets the bitkub client for grid trading
func (s *BotServiceImpl) SetTradingClient(client *bitkub.Client) {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	s.tradingClient = client
}

// SetExchangeManager sets the exchange manager for multi-exchange support
func (s *BotServiceImpl) SetExchangeManager(em *exchange.ExchangeManager) {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()
	s.exchangeManager = em
}

// gridParams is an immutable snapshot of the grid configuration handed to the
// grid loop, so the loop never reads fields that Stop/Start mutate.
type gridParams struct {
	symbol     string
	quantity   float64
	gridLevels int
	lowerPrice float64
	upperPrice float64
	// investment caps the quote notional of grid inventory (0 = no cap).
	investment float64
	// maxPosition caps base inventory (0 = quantity * gridLevels).
	maxPosition float64
}

// resolveBotMode maps optional start parameters to the operating mode.
func resolveBotMode(params *input.BotStartParams) model.BotMode {
	if params != nil && params.BotMode != "" {
		switch params.BotMode {
		case "GRID":
			return model.BotModeGrid
		case "AUTO":
			return model.BotModeAuto
		default:
			return model.BotModeSignal
		}
	}
	if params != nil && params.Symbol != "" {
		return model.BotModeGrid
	}
	return model.BotModeSignal
}

// validateGridParams rejects grid configurations that would make the grid
// loop place orders on every tick (for example gridLevels=0 divides the range
// by zero and turns every price into a BUY signal).
func validateGridParams(params *input.BotStartParams) error {
	if params == nil {
		return fmt.Errorf("grid mode requires parameters")
	}
	if params.Symbol == "" {
		return fmt.Errorf("grid mode requires a symbol")
	}
	if params.Quantity <= 0 {
		return fmt.Errorf("grid quantity must be greater than 0")
	}
	if params.GridLevels < 1 {
		return fmt.Errorf("gridLevels must be at least 1")
	}
	if params.LowerPrice <= 0 || params.UpperPrice <= 0 {
		return fmt.Errorf("grid prices must be greater than 0")
	}
	if params.LowerPrice >= params.UpperPrice {
		return fmt.Errorf("lowerPrice (%.2f) must be less than upperPrice (%.2f)", params.LowerPrice, params.UpperPrice)
	}
	return nil
}

// withSignalDefaults fills unset signal thresholds with conservative defaults.
func withSignalDefaults(cfg input.SignalConfig) input.SignalConfig {
	if cfg.MinStrength <= 0 {
		cfg.MinStrength = 0.5
	}
	if cfg.StopLossPct <= 0 {
		cfg.StopLossPct = 0.05 // 5%
	}
	if cfg.TakeProfitPct <= 0 {
		cfg.TakeProfitPct = 0.10 // 10%
	}
	return cfg
}

// Start starts the trading bot with optional grid trading parameters
func (s *BotServiceImpl) Start(ctx context.Context, params *input.BotStartParams) error {
	s.runningMu.Lock()
	defer s.runningMu.Unlock()

	if s.isRunning {
		return fmt.Errorf("bot is already running")
	}
	if s.stopPending {
		return fmt.Errorf("previous run is still stopping")
	}
	s.runLoops = &sync.WaitGroup{}

	mode := resolveBotMode(params)
	if mode == model.BotModeGrid {
		if err := validateGridParams(params); err != nil {
			return fmt.Errorf("invalid grid parameters: %w", err)
		}
	}

	if err := s.botStatusRepo.SetActive(ctx, true); err != nil {
		return fmt.Errorf("failed to update bot status: %w", err)
	}

	// Traded symbols accumulate (and persist across restarts): an earlier
	// run's orders may still be open, so a new Start never drops them from
	// the kill switch's drain list. No symbol record, no run.
	startSymbol := ""
	if params != nil {
		startSymbol = params.Symbol
	}
	if mode == model.BotModeAuto && params != nil {
		startSymbol = autoModeSymbol(params.SignalConfig)
	}
	if err := s.noteTradedSymbol(ctx, startSymbol); err != nil {
		if rbErr := s.botStatusRepo.SetActive(ctx, false); rbErr != nil {
			logger.Error("Failed to roll back bot status", "error", rbErr)
		}
		return fmt.Errorf("failed to record traded symbol: %w", err)
	}

	runCtx, cancel := context.WithCancel(context.Background())
	s.ctx, s.cancel = runCtx, cancel
	s.isRunning = true
	s.startedAt = time.Now()
	s.positions = make(map[string]*positionInfo)
	s.botMode = mode
	if params != nil && (params.BotMode == "SIGNAL" || params.BotMode == "AUTO") {
		s.signalConfig = params.SignalConfig
	}

	// Broadcast status update
	startedAt := s.startedAt
	s.broadcaster.BroadcastBotStatus(&model.BotStatus{
		IsActive:  true,
		StartedAt: &startedAt,
		BotMode:   s.botMode,
	})

	// Start appropriate mode. Each loop receives its own context and config
	// snapshot so a later Stop/Start cannot leak state into a stale goroutine.
	switch s.botMode {
	case model.BotModeGrid:
		s.startGridMode(runCtx, params)
	case model.BotModeSignal:
		s.startSignalMode(runCtx)
	case model.BotModeAuto:
		s.startAutoMode(runCtx)
	}

	return nil
}

// startGridMode must be called with runningMu held.
func (s *BotServiceImpl) startGridMode(ctx context.Context, params *input.BotStartParams) {
	s.symbol = params.Symbol
	s.quantity = params.Quantity
	s.gridLevels = params.GridLevels
	s.lowerPrice = params.LowerPrice
	s.upperPrice = params.UpperPrice
	s.investment = params.Investment
	s.tradesCount = 0
	s.totalProfit = 0

	grid := gridParams{
		symbol:     params.Symbol,
		quantity:   params.Quantity,
		gridLevels: params.GridLevels,
		lowerPrice: params.LowerPrice,
		upperPrice: params.UpperPrice,
		investment: params.Investment,
	}

	// Test API connection first
	if s.tradingClient != nil {
		_, err := s.tradingClient.GetBalances()
		if err != nil {
			logger.Error("Failed to connect to exchange for grid trading", "error", err)
		}
	}

	s.broadcaster.BroadcastBotActivity(&model.BotActivity{
		Timestamp: time.Now(),
		Activity:  "STARTED",
		Symbol:    params.Symbol,
		Message:   "Grid bot started",
		Level:     "success",
	})

	logger.Info("Grid trading bot started", "symbol", grid.symbol, "grid_levels", grid.gridLevels)
	s.goLoop(func() { s.gridTradingLoop(ctx, grid) })
}

// startSignalMode must be called with runningMu held.
func (s *BotServiceImpl) startSignalMode(ctx context.Context) {
	s.broadcaster.BroadcastBotActivity(&model.BotActivity{
		Timestamp: time.Now(),
		Activity:  "STARTED",
		Message:   "Signal bot started — listening for signals",
		Level:     "info",
	})

	logger.Info("Trading bot started (signal mode)")
	cfg := withSignalDefaults(s.signalConfig)
	s.goLoop(func() { s.listenForOrderSignals(ctx, cfg) })
}

// startAutoMode must be called with runningMu held.
func (s *BotServiceImpl) startAutoMode(ctx context.Context) {
	symbol := autoModeSymbol(s.signalConfig)
	s.symbol = symbol

	s.broadcaster.BroadcastBotActivity(&model.BotActivity{
		Timestamp: time.Now(),
		Activity:  "STARTED",
		Symbol:    symbol,
		Message: fmt.Sprintf("Auto-adjust bot started (risk: %s, SL: %.1f%%, TP: %.1f%%)",
			s.signalConfig.RiskLevel, s.signalConfig.StopLossPct*100, s.signalConfig.TakeProfitPct*100),
		Level: "success",
	})

	logger.Info("Auto-adjust bot started", "symbol", symbol, "risk", s.signalConfig.RiskLevel)
	cfg := withSignalDefaults(s.signalConfig)
	s.goLoop(func() { s.autoTradingLoop(ctx, cfg) })
}

// ErrBotStopTimeout means Stop cancelled the run but a trading loop did not
// exit within botStopJoinTimeout, so an order may still be in flight.
var ErrBotStopTimeout = errors.New("bot stop: trading loop did not exit in time")

// botStopJoinTimeout bounds how long Stop waits for the loops to exit.
var botStopJoinTimeout = 30 * time.Second

// autoModeSymbol is the symbol an auto-mode run trades.
func autoModeSymbol(cfg input.SignalConfig) string {
	if cfg.Symbol == "" {
		return "BTCUSDT"
	}
	return cfg.Symbol
}

// tradedSymbolPersistTimeout bounds one durable symbol write. Start makes it
// while holding runningMu, so a stalled database must not hold that lock (and
// with it Stop) for long.
var tradedSymbolPersistTimeout = 3 * time.Second

// noteTradedSymbol records a symbol the bot may hold orders on, durably when
// the status repository can store it. Callers place no order for the symbol
// if this fails.
func (s *BotServiceImpl) noteTradedSymbol(ctx context.Context, symbol string) error {
	if symbol == "" {
		return nil
	}
	s.tradedMu.Lock()
	known := s.tradedSymbols[symbol]
	s.tradedMu.Unlock()
	if known {
		return nil
	}
	// The write runs without tradedMu so status reads are not stuck behind it.
	if store, ok := s.botStatusRepo.(repository.TradedSymbolStore); ok {
		writeCtx, cancel := context.WithTimeout(ctx, tradedSymbolPersistTimeout)
		err := store.AddTradedSymbol(writeCtx, symbol)
		cancel()
		if err != nil {
			return err
		}
	}
	s.tradedMu.Lock()
	defer s.tradedMu.Unlock()
	if s.tradedSymbols == nil {
		s.tradedSymbols = make(map[string]bool)
	}
	s.tradedSymbols[symbol] = true
	return nil
}

// tradedSymbolList returns every symbol traded by this process or, when
// persisted, by an earlier one.
func (s *BotServiceImpl) tradedSymbolList(ctx context.Context) ([]string, error) {
	seen := map[string]bool{}
	if store, ok := s.botStatusRepo.(repository.TradedSymbolStore); ok {
		stored, err := store.ListTradedSymbols(ctx)
		if err != nil {
			return nil, err
		}
		for _, sym := range stored {
			seen[sym] = true
		}
	}
	s.tradedMu.Lock()
	for sym := range s.tradedSymbols {
		seen[sym] = true
	}
	s.tradedMu.Unlock()
	out := make([]string, 0, len(seen))
	for sym := range seen {
		out = append(out, sym)
	}
	sort.Strings(out)
	return out, nil
}

// unresolvedOrder is a submission whose outcome no loop will reconcile any
// more because its run stopped.
type unresolvedOrder struct {
	symbol        string
	clientOrderID string
	submittedAt   time.Time
}

// botStopReconcileTimeout bounds the order lookups one Stop makes.
var botStopReconcileTimeout = 5 * time.Second

// afterStopJoin is a test seam run after Stop has joined the loops.
var afterStopJoin = func() {}

// parkUnknownOrders hands a stopping loop's unknown submissions to Stop.
// Confirmed orders are skipped: the exchange lists them as open orders.
func (s *BotServiceImpl) parkUnknownOrders(symbol string, orders ...*gridPendingOrder) {
	s.tradedMu.Lock()
	defer s.tradedMu.Unlock()
	for _, o := range orders {
		if o == nil || !o.unknown {
			continue
		}
		if s.unresolved == nil {
			s.unresolved = make(map[string]unresolvedOrder)
		}
		s.unresolved[o.clientOrderID] = unresolvedOrder{symbol: symbol, clientOrderID: o.clientOrderID, submittedAt: o.submittedAt}
	}
}

// reconcileUnresolved looks every parked submission up with a context of its
// own (the run's is cancelled). An order the exchange knows in any state is
// resolved: an open one is on the book for the kill switch to cancel. One
// still not found after gridUnknownGrace never reached the exchange.
func (s *BotServiceImpl) reconcileUnresolved() {
	s.tradedMu.Lock()
	orders := make([]unresolvedOrder, 0, len(s.unresolved))
	for _, o := range s.unresolved {
		orders = append(orders, o)
	}
	s.tradedMu.Unlock()
	if len(orders) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), botStopReconcileTimeout)
	defer cancel()
	for _, o := range orders {
		report, err := s.lookupFor(o.symbol).lookup(ctx, o.clientOrderID, o.submittedAt)
		if err != nil {
			logger.Warn("Unknown order still unresolved after stop", "symbol", o.symbol, "clientOrderId", o.clientOrderID, "error", err)
			continue
		}
		if report.state == gridOrderNotFound && time.Since(o.submittedAt) < gridUnknownGrace {
			continue
		}
		s.tradedMu.Lock()
		delete(s.unresolved, o.clientOrderID)
		s.tradedMu.Unlock()
	}
}

func (s *BotServiceImpl) lookupFor(symbol string) gridExchange {
	if s.orderLookup != nil {
		return s.orderLookup(symbol)
	}
	s.runningMu.RLock()
	defer s.runningMu.RUnlock()
	return &liveGridExchange{symbol: symbol, manager: s.exchangeManager, client: s.tradingClient}
}

func (s *BotServiceImpl) unresolvedOrderList() []model.UnresolvedOrder {
	s.tradedMu.Lock()
	defer s.tradedMu.Unlock()
	out := make([]model.UnresolvedOrder, 0, len(s.unresolved))
	for _, o := range s.unresolved {
		out = append(out, model.UnresolvedOrder{Symbol: o.symbol, ClientOrderID: o.clientOrderID})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ClientOrderID < out[j].ClientOrderID })
	return out
}

// goLoop runs a trading loop of the current run. Callers hold runningMu.
func (s *BotServiceImpl) goLoop(fn func()) {
	if s.runLoops == nil {
		s.runLoops = &sync.WaitGroup{}
	}
	wg := s.runLoops
	wg.Add(1)
	go func() {
		defer wg.Done()
		fn()
	}()
}

// Stop stops the trading bot and waits for its loops to exit. The loops take
// runningMu, so the wait happens after the lock is released.
func (s *BotServiceImpl) Stop(ctx context.Context) error {
	s.runningMu.Lock()
	switch {
	case s.isRunning:
		s.cancel()
		s.isRunning = false
		// Reset grid trading state
		s.symbol = ""
		s.quantity = 0
		s.gridLevels = 0
		s.lowerPrice = 0
		s.upperPrice = 0
		s.investment = 0
		s.signalConfig = input.SignalConfig{}
		s.positions = make(map[string]*positionInfo)

		wg := s.runLoops
		joined := make(chan struct{})
		go func() {
			if wg != nil {
				wg.Wait()
			}
			close(joined)
		}()
		s.runJoined = joined
		s.stopPending = true
	case !s.stopPending:
		s.runningMu.Unlock()
		// A kill retry: re-check what an earlier stop left unresolved.
		s.reconcileUnresolved()
		return input.ErrBotNotRunning
	}
	joined := s.runJoined
	s.runningMu.Unlock()

	select {
	case <-joined:
	case <-time.After(botStopJoinTimeout):
		logger.Error("Trading loop did not exit after stop", "timeout", botStopJoinTimeout)
		return ErrBotStopTimeout
	}
	afterStopJoin()
	s.reconcileUnresolved()

	s.runningMu.Lock()
	// Only the run this call joined may be finalized: a later run may have
	// started and begun stopping while this waiter was paused.
	finish := s.stopPending && !s.stopFinalizing && s.runJoined == joined
	s.stopFinalizing = s.stopFinalizing || finish
	s.runningMu.Unlock()
	if !finish {
		return nil // a concurrent Stop is completing it
	}
	// Start stays refused (stopPending) until the stopped status is written,
	// so a new run's "active" cannot be overwritten by this stop.
	defer func() {
		s.runningMu.Lock()
		s.stopPending, s.stopFinalizing = false, false
		s.runningMu.Unlock()
	}()

	if err := s.botStatusRepo.SetActive(ctx, false); err != nil {
		return fmt.Errorf("failed to update bot status: %w", err)
	}

	// Broadcast status update
	stoppedAt := time.Now()
	s.broadcaster.BroadcastBotStatus(&model.BotStatus{
		IsActive:  false,
		StoppedAt: &stoppedAt,
	})

	logger.Info("Trading bot stopped")
	return nil
}

// GetStatus retrieves the current bot status
func (s *BotServiceImpl) GetStatus(ctx context.Context) (*model.BotStatus, error) {
	status, err := s.botStatusRepo.Get(ctx)
	if err != nil {
		return nil, err
	}
	// Fewer symbols than were traded would let the kill switch skip live
	// orders, so an unreadable list fails the status read.
	symbols, err := s.tradedSymbolList(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to read traded symbols: %w", err)
	}
	status.Symbols = symbols
	status.UnresolvedOrders = s.unresolvedOrderList()

	s.runningMu.RLock()
	status.IsActive = s.isRunning
	// Include trading stats if available
	status.TotalTrades = s.botStatus.TotalTrades
	status.TotalProfit = s.botStatus.TotalProfit
	status.BotMode = s.botMode
	if s.isRunning {
		status.Symbol = s.symbol
		startedAt := s.startedAt
		status.StartedAt = &startedAt
	}
	s.runningMu.RUnlock()

	return status, nil
}

// IsRunning checks if the bot is currently running
func (s *BotServiceImpl) IsRunning(ctx context.Context) bool {
	s.runningMu.RLock()
	defer s.runningMu.RUnlock()
	return s.isRunning
}

func (s *BotServiceImpl) listenForOrderSignals(ctx context.Context, cfg input.SignalConfig) {
	logger.Info("Listening for order signals from strategy service")

	signalChan, err := s.orderSignalSub.SubscribeOrderSignals(ctx)
	if err != nil {
		logger.Error("Failed to subscribe to order signals", "error", err)
		return
	}
	orders := newSignalOrderBook()
	defer s.parkSignalOrders(orders)

	for {
		select {
		case <-ctx.Done():
			logger.Info("Signal listener stopped (context cancelled)")
			return
		case signal, ok := <-signalChan:
			if !ok {
				logger.Error("Signal channel closed")
				return
			}

			if signal.Strength < cfg.MinStrength {
				s.broadcaster.BroadcastBotActivity(&model.BotActivity{
					Timestamp: time.Now(),
					Activity:  "FILTERED",
					Symbol:    string(signal.Symbol),
					Message:   fmt.Sprintf("Signal filtered (strength %.2f < %.2f)", signal.Strength, cfg.MinStrength),
					Level:     "warning",
				})
				continue
			}

			s.broadcaster.BroadcastBotActivity(&model.BotActivity{
				Timestamp: time.Now(),
				Activity:  "SIGNAL_RECEIVED",
				Symbol:    string(signal.Symbol),
				Message:   fmt.Sprintf("%s signal for %s (strength %.2f): %s", signal.Side, signal.Symbol, signal.Strength, signal.Reason),
				Level:     "info",
			})

			// Execute the trade
			s.executeSignalTrade(ctx, orders, string(signal.Symbol), string(signal.Side), cfg)
		}
	}
}

// parkSignalOrders hands a stopping signal/auto loop's unknown orders to Stop.
func (s *BotServiceImpl) parkSignalOrders(orders *signalOrderBook) {
	for symbol, o := range orders.pending {
		s.parkUnknownOrders(symbol, o)
	}
}

// autoTradingLoop combines signal-based entry with auto-adjust stop-loss/take-profit
func (s *BotServiceImpl) autoTradingLoop(ctx context.Context, cfg input.SignalConfig) {
	signalChan, err := s.orderSignalSub.SubscribeOrderSignals(ctx)
	if err != nil {
		logger.Error("Failed to subscribe to order signals for auto mode", "error", err)
		return
	}
	orders := newSignalOrderBook()
	defer s.parkSignalOrders(orders)

	// Price check ticker for stop-loss/take-profit monitoring
	priceTicker := time.NewTicker(10 * time.Second)
	defer priceTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			logger.Info("Auto bot stopped (context cancelled)")
			return
		case signal, ok := <-signalChan:
			if !ok {
				return
			}
			if signal.Strength < cfg.MinStrength {
				continue
			}
			s.handleAutoSignal(ctx, orders, signal, cfg)
		case <-priceTicker.C:
			s.checkStopLossTakeProfit(ctx, orders, cfg)
		}
	}
}

func (s *BotServiceImpl) hasPosition(symbol string) bool {
	s.runningMu.RLock()
	defer s.runningMu.RUnlock()
	_, exists := s.positions[symbol]
	return exists
}

func (s *BotServiceImpl) handleAutoSignal(ctx context.Context, orders *signalOrderBook, signal *output.OrderSignal, cfg input.SignalConfig) {
	symbol := string(signal.Symbol)

	// BUY signal — open position
	if signal.Side == model.SideBuy {
		if s.hasPosition(symbol) {
			s.broadcaster.BroadcastBotActivity(&model.BotActivity{
				Timestamp: time.Now(),
				Activity:  "WAITING",
				Symbol:    symbol,
				Message:   "Already have open position, skipping BUY",
				Level:     "info",
			})
			return
		}

		s.executeSignalTrade(ctx, orders, symbol, string(signal.Side), cfg)
		return
	}

	// SELL signal — close position if open
	if signal.Side == model.SideSell {
		if !s.hasPosition(symbol) {
			return // no position to close
		}

		if !s.executeSignalTrade(ctx, orders, symbol, string(signal.Side), cfg) {
			return // keep tracking the position; the exit did not go through
		}

		s.runningMu.Lock()
		delete(s.positions, symbol)
		s.runningMu.Unlock()

		s.broadcaster.BroadcastBotActivity(&model.BotActivity{
			Timestamp: time.Now(),
			Activity:  "CLOSED",
			Symbol:    symbol,
			Message:   fmt.Sprintf("Position closed (sold %s on SELL signal)", symbol),
			Level:     "success",
		})
	}
}

func (s *BotServiceImpl) checkStopLossTakeProfit(ctx context.Context, orders *signalOrderBook, cfg input.SignalConfig) {
	s.runningMu.RLock()
	positions := make(map[string]positionInfo)
	for k, v := range s.positions {
		positions[k] = *v
	}
	isRunning := s.isRunning
	manager := s.exchangeManager
	s.runningMu.RUnlock()

	if !isRunning || len(positions) == 0 || manager == nil {
		return
	}

	for symbol, pos := range positions {
		if pos.entryPrice <= 0 {
			continue // cannot compute PnL against a zero/negative entry
		}
		ticker, err := manager.GetTicker(ctx, symbol)
		if err != nil || ticker == nil || ticker.LastPrice <= 0 {
			continue
		}
		currentPrice := ticker.LastPrice

		pnlPct := (currentPrice - pos.entryPrice) / pos.entryPrice

		// Stop-loss check
		if pnlPct <= -cfg.StopLossPct {
			s.broadcaster.BroadcastBotActivity(&model.BotActivity{
				Timestamp: time.Now(),
				Activity:  "STOP_LOSS",
				Symbol:    symbol,
				Message:   fmt.Sprintf("Stop-loss triggered: %.2f%% loss (threshold %.1f%%)", pnlPct*100, cfg.StopLossPct*100),
				Level:     "error",
			})
			if s.executeSignalTrade(ctx, orders, symbol, "SELL", cfg) {
				s.runningMu.Lock()
				delete(s.positions, symbol)
				s.runningMu.Unlock()
			}
			continue
		}

		// Take-profit check
		if pnlPct >= cfg.TakeProfitPct {
			s.broadcaster.BroadcastBotActivity(&model.BotActivity{
				Timestamp: time.Now(),
				Activity:  "TAKE_PROFIT",
				Symbol:    symbol,
				Message:   fmt.Sprintf("Take-profit triggered: %.2f%% gain (threshold %.1f%%)", pnlPct*100, cfg.TakeProfitPct*100),
				Level:     "success",
			})
			if s.executeSignalTrade(ctx, orders, symbol, "SELL", cfg) {
				s.runningMu.Lock()
				delete(s.positions, symbol)
				s.runningMu.Unlock()
			}
		}
	}
}

// fetchPrice returns the last traded price from whichever exchange client is
// configured. A non-positive price is treated as an error so no order is ever
// sized or placed against a zero quote.
func fetchPrice(ctx context.Context, manager *exchange.ExchangeManager, client *bitkub.Client, symbol string) (float64, error) {
	var price float64
	switch {
	case manager != nil:
		ticker, err := manager.GetTicker(ctx, symbol)
		if err != nil {
			return 0, err
		}
		if ticker != nil {
			price = ticker.LastPrice
		}
	case client != nil:
		ticker, err := client.GetTicker(symbol)
		if err != nil {
			return 0, err
		}
		if ticker != nil {
			price = ticker.LastPrice
		}
	default:
		return 0, fmt.Errorf("no exchange client configured")
	}
	if price <= 0 {
		return 0, fmt.Errorf("invalid price %.8f for %s", price, symbol)
	}
	return price, nil
}

// executeSignalTrade places one signal-driven order and reports whether an
// order of this side filled (see signalTradeStep).
func (s *BotServiceImpl) executeSignalTrade(ctx context.Context, orders *signalOrderBook, symbol string, side string, cfg input.SignalConfig) bool {
	if err := s.noteTradedSymbol(ctx, symbol); err != nil {
		logger.Error("Signal trade skipped: traded symbol not recorded", "symbol", symbol, "error", err)
		return false
	}
	// Determine quantity from risk config
	quantity := cfg.Quantity
	if quantity <= 0 {
		quantity = 0.001 // default minimum
	}

	s.runningMu.RLock()
	manager := s.exchangeManager
	client := s.tradingClient
	s.runningMu.RUnlock()

	if manager == nil && client == nil {
		logger.Warn("No exchange client for signal trade")
		return false
	}

	currentPrice, err := fetchPrice(ctx, manager, client, symbol)
	if err != nil {
		logger.Info("Error getting ticker for signal trade", "error", err)
		return false
	}

	// Never place an order after the bot was stopped while we were waiting
	// on the ticker.
	if ctx.Err() != nil {
		return false
	}

	ex := &liveGridExchange{symbol: symbol, manager: manager, client: client}
	return s.signalTradeStep(ctx, orders, symbol, side, quantity, currentPrice, ex)
}

// gridTradingLoop is the main grid trading loop. The gridBook is owned by
// this goroutine only, so it needs no locking.
func (s *BotServiceImpl) gridTradingLoop(ctx context.Context, grid gridParams) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	book := newGridBook(grid)
	defer func() { s.parkUnknownOrders(grid.symbol, book.pending) }()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Execute grid trading logic
			s.executeGridTrading(ctx, grid, book)
		}
	}
}

// gridAction decides what the grid does at a price: "BUY" in the bottom grid
// band, "SELL" in the top band, "" (wait) otherwise or for invalid input.
func gridAction(grid gridParams, currentPrice float64) (string, float64) {
	if grid.gridLevels < 1 || grid.upperPrice <= grid.lowerPrice || currentPrice <= 0 {
		return "", 0
	}
	gridSize := (grid.upperPrice - grid.lowerPrice) / float64(grid.gridLevels)
	switch {
	case currentPrice <= grid.lowerPrice+gridSize:
		return "BUY", gridSize
	case currentPrice >= grid.upperPrice-gridSize:
		return "SELL", gridSize
	default:
		return "", gridSize
	}
}

// gridLevel returns the grid band index (0 = bottom) that a price falls in,
// clamped to [0, gridLevels-1].
func gridLevel(grid gridParams, price float64) int {
	if grid.gridLevels < 1 || grid.upperPrice <= grid.lowerPrice {
		return 0
	}
	gridSize := (grid.upperPrice - grid.lowerPrice) / float64(grid.gridLevels)
	level := int(math.Floor((price - grid.lowerPrice) / gridSize))
	if level < 0 {
		return 0
	}
	if level >= grid.gridLevels {
		return grid.gridLevels - 1
	}
	return level
}

// positionEpsilon absorbs float rounding when comparing base quantities.
const positionEpsilon = 1e-12

// gridBook is the per-run inventory and level state of the grid loop.
//
//   - Idempotent levels: a BUY is placed at most once per grid level. The level
//     stays "held" until a SELL releases it, so a price that sits in the buy
//     band no longer submits an order on every tick.
//   - Position cap: base inventory never exceeds maxPosition (default
//     quantity * gridLevels, i.e. one fill per level).
//   - Exposure cap: when Investment > 0, the quote notional of the inventory
//     (position * price) never exceeds it.
//   - No naked sells: a SELL needs at least one quantity of inventory bought
//     by this run.
type gridBook struct {
	position    float64
	heldLevels  map[int]bool
	maxPosition float64
	maxExposure float64
	// levelQuantity is the order size of one grid level.
	levelQuantity float64

	// pending is the order whose outcome is not final yet (accepted but not
	// filled, or unknown after a timeout). While it is set the grid places
	// no other order; see reconcileGridOrder.
	pending *gridPendingOrder
	runID   string
	seq     int
	now     func() time.Time
}

func newGridBook(grid gridParams) *gridBook {
	maxPosition := grid.maxPosition
	if maxPosition <= 0 {
		maxPosition = grid.quantity * float64(grid.gridLevels)
	}
	return &gridBook{
		heldLevels:    make(map[int]bool),
		maxPosition:   maxPosition,
		maxExposure:   grid.investment,
		levelQuantity: grid.quantity,
		runID:         strconv.FormatInt(time.Now().UnixNano(), 36),
		now:           time.Now,
	}
}

// check reports whether an order may be placed and, if not, why.
func (b *gridBook) check(side string, level int, quantity, price float64) (bool, string) {
	switch side {
	case "BUY":
		if b.heldLevels[level] {
			return false, fmt.Sprintf("level %d already filled", level)
		}
		if b.position+quantity > b.maxPosition+positionEpsilon {
			return false, fmt.Sprintf("position cap %.8f reached", b.maxPosition)
		}
		if b.maxExposure > 0 && (b.position+quantity)*price > b.maxExposure+positionEpsilon {
			return false, fmt.Sprintf("exposure cap %.2f reached", b.maxExposure)
		}
		return true, ""
	case "SELL":
		if b.position+positionEpsilon < quantity {
			return false, "no grid inventory to sell"
		}
		return true, ""
	default:
		return false, "unknown side"
	}
}

// record applies a confirmed fill to the book.
func (b *gridBook) record(side string, level int, quantity float64) {
	switch side {
	case "BUY":
		b.position += quantity
		b.heldLevels[level] = true
	case "SELL":
		b.position -= quantity
		if b.position < positionEpsilon {
			b.position = 0
		}
		// Release the lowest held level so the grid can buy it again. A partial
		// SELL fill does not free a level.
		if quantity+positionEpsilon < b.levelQuantity {
			return
		}
		lowest := -1
		for l := range b.heldLevels {
			if lowest == -1 || l < lowest {
				lowest = l
			}
		}
		if lowest >= 0 {
			delete(b.heldLevels, lowest)
		}
	}
}

// executeGridTrading fetches the price and runs one grid step.
func (s *BotServiceImpl) executeGridTrading(ctx context.Context, grid gridParams, book *gridBook) {
	if err := s.noteTradedSymbol(ctx, grid.symbol); err != nil {
		logger.Error("Grid tick skipped: traded symbol not recorded", "symbol", grid.symbol, "error", err)
		return
	}
	s.runningMu.RLock()
	running := s.isRunning
	client := s.tradingClient
	manager := s.exchangeManager
	s.runningMu.RUnlock()

	if !running || ctx.Err() != nil || grid.symbol == "" {
		return
	}
	if client == nil && manager == nil {
		logger.Warn("No exchange client configured for grid trading")
		return
	}

	currentPrice, err := fetchPrice(ctx, manager, client, grid.symbol)
	if err != nil {
		logger.Info("Error getting ticker", "error", err)
		return
	}

	s.gridStep(ctx, grid, book, currentPrice, &liveGridExchange{symbol: grid.symbol, manager: manager, client: client})
}

// PortfolioServiceImpl implements the PortfolioService interface
type PortfolioServiceImpl struct {
	portfolioRepo repository.PortfolioRepository
}

func NewPortfolioService(portfolioRepo repository.PortfolioRepository) input.PortfolioHandler {
	return &PortfolioServiceImpl{
		portfolioRepo: portfolioRepo,
	}
}

// GetPortfolio retrieves the entire portfolio
func (s *PortfolioServiceImpl) GetPortfolio(ctx context.Context) ([]*model.Portfolio, error) {
	return s.portfolioRepo.GetAll(ctx)
}

// GetPortfolioBySymbol retrieves portfolio for a specific symbol
func (s *PortfolioServiceImpl) GetPortfolioBySymbol(ctx context.Context, symbol model.TradeSymbol) (*model.Portfolio, error) {
	return s.portfolioRepo.Get(ctx, symbol)
}

// UpdatePortfolioAfterTrade updates portfolio after a trade is executed
func (s *PortfolioServiceImpl) UpdatePortfolioAfterTrade(ctx context.Context, trade *model.TradeHistory) error {
	portfolio, err := s.portfolioRepo.Get(ctx, trade.Symbol)
	if err != nil {
		// Create new portfolio if not exists
		portfolio = &model.Portfolio{
			Symbol: trade.Symbol,
		}
	}

	if trade.Side == model.SideBuy {
		// Update balance and average buy price
		totalCost := portfolio.Balance*portfolio.AvgBuyPrice + trade.Total
		portfolio.Balance += trade.Quantity
		if portfolio.Balance > 0 {
			portfolio.AvgBuyPrice = totalCost / portfolio.Balance
		}
	} else {
		// Sell
		portfolio.Balance -= trade.Quantity
	}

	portfolio.UpdatedAt = time.Now()
	return s.portfolioRepo.Update(ctx, portfolio)
}

// TradeHistoryServiceImpl implements the TradeHistoryService interface
type TradeHistoryServiceImpl struct {
	tradeHistoryRepo repository.TradeHistoryRepository
}

// NewTradeHistoryService creates a new trade history service
func NewTradeHistoryService(tradeHistoryRepo repository.TradeHistoryRepository) input.TradeHistoryHandler {
	return &TradeHistoryServiceImpl{
		tradeHistoryRepo: tradeHistoryRepo,
	}
}

// GetTradeHistory retrieves trade history
func (s *TradeHistoryServiceImpl) GetTradeHistory(ctx context.Context, limit int) ([]*model.TradeHistory, error) {
	return s.tradeHistoryRepo.GetAll(ctx, limit)
}

// GetTradeHistoryBySymbol retrieves trade history for a specific symbol
func (s *TradeHistoryServiceImpl) GetTradeHistoryBySymbol(ctx context.Context, symbol model.TradeSymbol, limit int) ([]*model.TradeHistory, error) {
	return s.tradeHistoryRepo.GetBySymbol(ctx, symbol, limit)
}
