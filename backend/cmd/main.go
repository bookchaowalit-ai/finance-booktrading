package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"trading-bot-system/backend/internal/adapter/database"
	"trading-bot-system/backend/internal/adapter/dex"
	"trading-bot-system/backend/internal/adapter/exchange"
	"trading-bot-system/backend/internal/adapter/grpcserver"
	httpadapter "trading-bot-system/backend/internal/adapter/http"
	"trading-bot-system/backend/internal/adapter/redis"
	repositoryadapter "trading-bot-system/backend/internal/adapter/repository"
	"trading-bot-system/backend/internal/adapter/websocket"
	"trading-bot-system/backend/internal/config"
	"trading-bot-system/backend/internal/domain/model"
	"trading-bot-system/backend/internal/domain/service"
	"trading-bot-system/backend/internal/logger"
)

func main() {
	// Load configuration
	cfg := config.LoadConfig()

	// Validate required configuration
	if err := cfg.Validate(); err != nil {
		logger.Fatal("Configuration error", "error", err)
	}

	// Initialize Redis adapter
	redisAdapter := redis.NewRedisAdapter(
		cfg.Redis.Host,
		cfg.Redis.Port,
		cfg.Redis.Password,
		cfg.Redis.DB,
	)

	// Initialize PostgreSQL database
	db, err := database.NewPostgresDB(database.Config{
		Host:     cfg.Database.Host,
		Port:     cfg.Database.Port,
		User:     cfg.Database.User,
		Password: cfg.Database.Password,
		DBName:   cfg.Database.DBName,
		SSLMode:  cfg.Database.SSLMode,
	})
	if err != nil {
		logger.Fatal("Failed to connect to database", "error", err)
	}
	defer db.Close()

	// Run database migrations
	logger.Info("Running database migrations...")
	databaseURL := database.BuildDatabaseURL(
		cfg.Database.Host,
		cfg.Database.Port,
		cfg.Database.User,
		cfg.Database.Password,
		cfg.Database.DBName,
		cfg.Database.SSLMode,
	)
	if err := database.RunMigrations(databaseURL); err != nil {
		logger.Info("Migration warning", "error", err)
		// Continue anyway - migrations might have already run
	}

	// Initialize repositories (using PostgreSQL)
	orderRepo := repositoryadapter.NewPostgresOrderRepository(db.Pool)
	portfolioRepo := repositoryadapter.NewPostgresPortfolioRepository(db.Pool)
	tradeHistoryRepo := repositoryadapter.NewPostgresTradeHistoryRepository(db.Pool)
	botStatusRepo := repositoryadapter.NewPostgresBotStatusRepository(db.Pool)
	marketDataRepo := repositoryadapter.NewPostgresMarketDataRepository(db.Pool)
	apiKeyRepo := database.NewAPIKeyRepository(db.Pool)
	prefsRepo := database.NewUserPreferencesRepository(db.Pool)

	// Initialize Finance repositories
	financeAccountRepo := repositoryadapter.NewPostgresFinanceAccountRepository(db.Pool)
	financeCategoryRepo := repositoryadapter.NewPostgresFinanceCategoryRepository(db.Pool)
	financeTransactionRepo := repositoryadapter.NewPostgresFinanceTransactionRepository(db.Pool)
	financeBudgetRepo := repositoryadapter.NewPostgresFinanceBudgetRepository(db.Pool)
	financeGoalRepo := repositoryadapter.NewPostgresFinanceGoalRepository(db.Pool)
	financeAssetRepo := repositoryadapter.NewPostgresFinanceAssetRepository(db.Pool)
	financeLiabilityRepo := repositoryadapter.NewPostgresFinanceLiabilityRepository(db.Pool)
	financeSubscriptionRepo := repositoryadapter.NewPostgresFinanceSubscriptionRepository(db.Pool)
	financeDiaryRepo := repositoryadapter.NewPostgresFinanceDiaryRepository(db.Pool)
	netWorthHistoryRepo := repositoryadapter.NewPostgresNetWorthHistoryRepository(db.Pool)
	_ = repositoryadapter.NewPostgresRecurringTransactionRepository(db.Pool) // Initialized but not yet wired

	// Initialize exchange manager (handles multiple exchanges)
	exchangeManager := exchange.NewExchangeManager(&cfg.Exchange, apiKeyRepo)

	// Initialize exchange adapters (get credentials from APIKeys map)
	// Try Binance Global first, then fall back to Binance TH
	binanceKey := cfg.Exchange.APIKeys[string(config.ExchangeBinance)]
	binanceTHKey := cfg.Exchange.APIKeys[string(config.ExchangeBinanceTH)]
	var binanceStream *exchange.BinanceAdapter
	var binanceExecutor *exchange.BinanceOrderExecutor

	if binanceKey != nil && binanceKey.Enabled {
		// Use Binance Global credentials
		binanceStream = exchange.NewBinanceAdapter(
			binanceKey.APIKey,
			binanceKey.APISecret,
			binanceKey.UseTestnet,
		)
		binanceExecutor = exchange.NewBinanceOrderExecutor(
			binanceKey.APIKey,
			binanceKey.APISecret,
			binanceKey.UseTestnet,
		)
		logger.Info("Using Binance Global credentials for order execution")
	} else if binanceTHKey != nil && binanceTHKey.Enabled {
		// Fall back to Binance TH credentials
		binanceStream = exchange.NewBinanceAdapter(
			binanceTHKey.APIKey,
			binanceTHKey.APISecret,
			false,
		)
		binanceExecutor = exchange.NewBinanceOrderExecutorWithBaseURL(
			binanceTHKey.APIKey,
			binanceTHKey.APISecret,
			"https://api.binance.th",
		)
		logger.Info("Using Binance TH credentials for order execution (fallback)")
	} else {
		// No credentials available
		binanceStream = exchange.NewBinanceAdapter("", "", true)
		binanceExecutor = exchange.NewBinanceOrderExecutor("", "", true)
		logger.Warn("No exchange credentials configured — order execution will fail")
	}

	// Initialize WebSocket broadcaster
	wsBroadcaster := websocket.NewWebSocketBroadcaster(
		cfg.WebSocket.MaxClients,
		cfg.WebSocket.BroadcastQueue,
	)

	// Start WebSocket broadcaster goroutine
	go wsBroadcaster.Run()

	// Initialize services
	orderService := service.NewOrderService(
		orderRepo,
		tradeHistoryRepo,
		binanceExecutor,
		wsBroadcaster,
	)

	marketDataService := service.NewMarketDataService(
		binanceStream,
		marketDataRepo,
		redisAdapter,
		wsBroadcaster,
	)

	botService := service.NewBotService(
		botStatusRepo,
		redisAdapter,
		wsBroadcaster,
	)

	// Wire exchange manager to bot service for multi-exchange grid trading
	botService.SetExchangeManager(exchangeManager)

	portfolioService := service.NewPortfolioService(portfolioRepo)
	tradeHistoryService := service.NewTradeHistoryService(tradeHistoryRepo)

	// Initialize Finance services
	financeAccountService := service.NewFinanceAccountService(financeAccountRepo)
	financeCategoryService := service.NewFinanceCategoryService(financeCategoryRepo)
	financeTransactionService := service.NewFinanceTransactionService(financeTransactionRepo, financeAccountRepo, financeCategoryRepo)
	financeBudgetService := service.NewFinanceBudgetService(financeBudgetRepo, financeTransactionRepo)
	financeGoalService := service.NewFinanceGoalService(financeGoalRepo)
	financeAssetService := service.NewFinanceAssetService(financeAssetRepo)
	financeLiabilityService := service.NewFinanceLiabilityService(financeLiabilityRepo)
	financeSubscriptionService := service.NewFinanceSubscriptionService(financeSubscriptionRepo)
	financeDiaryService := service.NewFinanceDiaryService(financeDiaryRepo)
	financeCalculatorService := service.NewFinancialCalculatorService()
	netWorthService := service.NewNetWorthService(
		financeAccountRepo,
		financeAssetRepo,
		financeLiabilityRepo,
		netWorthHistoryRepo,
		financeTransactionRepo,
	)
	dashboardService := service.NewDashboardService(
		financeAccountRepo,
		financeTransactionRepo,
		financeBudgetRepo,
		financeGoalRepo,
		financeSubscriptionRepo,
		financeCategoryRepo,
		financeAssetRepo,
		financeLiabilityRepo,
		netWorthHistoryRepo,
	)

	// DEX/AMM Trading (initialized early for gRPC server)
	dexManager := dex.NewDEXManager(db.Pool, &cfg.Dex)
	dexCtx, dexCancel := context.WithTimeout(context.Background(), 10*time.Second)
	if err := dexManager.Initialize(dexCtx); err != nil {
		logger.Info("DEX manager initialization", "error", err)
	}
	dexCancel()
	// DEX wallets sign with keys sealed by DEX_WALLET_ENCRYPTION_KEY; with DEX
	// on and no key, refuse to start rather than run with wallets unusable.
	if cfg.Dex.Enabled {
		if err := service.ValidateDexWalletKey(); err != nil {
			logger.Error("DEX is enabled but the wallet encryption key is unusable", "error", err)
			os.Exit(1)
		}
	}
	walletService := service.NewWalletService(db.Pool, &cfg.Dex)
	txSigner := service.NewTxSigner(walletService)
	dexManager.SetSigner(txSigner)

	// Initialize gRPC server
	grpcServer := grpcserver.NewGRPCServer(
		cfg.GRPC.Port,
		orderService,
		botService,
		marketDataService,
		dexManager,
	)

	// Initialize new feature services
	paperEngine := service.NewPaperEngine(50000.0, 0.001, db.Pool) // $50k initial, 0.1% fee, persisted to PostgreSQL
	riskManager := service.NewRiskManager(nil, 50000.0)
	alertService := service.NewAlertService(nil)
	metricsService := service.NewMetricsService()
	backtestService := service.NewBacktestService()

	// Historical data service (syncs klines from Binance)
	historicalService := service.NewHistoricalDataService(db.Pool, []string{"BTCUSDT", "ETHUSDT"})
	historicalService.StartSync("1h")
	// Go durations use "h" for hours; the Binance kline interval remains "1d"
	// inside the service's per-symbol sync loop.
	historicalService.StartSync("24h")
	defer historicalService.StopSync()

	// Audit logging
	auditService := service.NewAuditService(db.Pool, 1000)

	// Price alert monitor
	priceAlertMonitor := service.NewPriceAlertMonitor(db.Pool, alertService)

	// Event bus for decoupled alert wiring
	eventBus := service.NewEventBus()

	// Wire paper engine to event bus so fills publish events
	paperEngine.SetEventBus(eventBus)

	// Wire alert service to events
	eventBus.Subscribe(service.EventBotStart, func(ctx context.Context, event service.Event) {
		symbol, _ := event.Data["symbol"].(string)
		alertService.SendBotStatusAlert(ctx, true, symbol, "")
	})
	eventBus.Subscribe(service.EventBotStop, func(ctx context.Context, event service.Event) {
		symbol, _ := event.Data["symbol"].(string)
		reason, _ := event.Data["reason"].(string)
		alertService.SendBotStatusAlert(ctx, false, symbol, reason)
	})
	eventBus.Subscribe(service.EventTradeExec, func(ctx context.Context, event service.Event) {
		side, _ := event.Data["side"].(string)
		symbol, _ := event.Data["symbol"].(string)
		qty, _ := event.Data["quantity"].(float64)
		price, _ := event.Data["price"].(float64)
		pnl, _ := event.Data["pnl"].(float64)
		alertService.SendTradeAlert(ctx, symbol, side, qty, price, pnl)
	})
	eventBus.Subscribe(service.EventRiskAlert, func(ctx context.Context, event service.Event) {
		metric, _ := event.Data["metric"].(string)
		value, _ := event.Data["value"].(string)
		limit, _ := event.Data["limit"].(string)
		alertService.SendRiskAlert(ctx, metric, value, limit)
	})
	eventBus.Subscribe(service.EventSystemError, func(ctx context.Context, event service.Event) {
		errMsg, _ := event.Data["error"].(string)
		contextVal, _ := event.Data["context"].(string)
		alertService.SendErrorAlert(ctx, fmt.Errorf("%s", errMsg), contextVal)
	})
	// Wire paper trade events to alert service
	eventBus.Subscribe(service.EventPaperTrade, func(ctx context.Context, event service.Event) {
		symbol, _ := event.Data["symbol"].(string)
		side, _ := event.Data["side"].(string)
		qty, _ := event.Data["quantity"].(float64)
		price, _ := event.Data["price"].(float64)
		pnl, _ := event.Data["pnl"].(float64)
		alertService.SendTradeAlert(ctx, symbol, side, qty, price, pnl)
	})

	// DCA Bot service (created here but handler wired after authHandler below)
	dcaService := service.NewDCABotService(db.Pool)

	// Feature handlers
	featureHandler := httpadapter.NewFeatureHandler(
		paperEngine,
		riskManager,
		alertService,
		metricsService,
		backtestService,
		auditService,
		priceAlertMonitor,
	)

	// Telegram Bot (if configured)
	telegramToken := os.Getenv("TELEGRAM_BOT_TOKEN")
	if telegramToken != "" {
		telegramBot := service.NewTelegramBotService(
			telegramToken,
			db.Pool,
			func() map[string]any {
				status, _ := botService.GetStatus(context.Background())
				if status == nil {
					return map[string]any{"isActive": false, "totalTrades": 0, "totalProfit": 0.0}
				}
				return map[string]any{
					"isActive":    status.IsActive,
					"startedAt":   status.StartedAt,
					"totalTrades": status.TotalTrades,
					"totalProfit": status.TotalProfit,
				}
			},
			func() map[string]any {
				portfolio, _ := portfolioService.GetPortfolio(context.Background())
				return map[string]any{"balances": portfolio}
			},
			func() map[string]any {
				return map[string]any{
					"winRate":      0.0,
					"profitFactor": 0.0,
					"avgWin":       0.0,
					"avgLoss":      0.0,
					"bestTrade":    0.0,
					"worstTrade":   0.0,
					"sharpeRatio":  0.0,
				}
			},
			func() error {
				return botService.Start(context.Background(), nil)
			},
			func() error {
				return botService.Stop(context.Background())
			},
			func() map[string]any {
				p := paperEngine.GetPortfolio()
				return map[string]any{
					"totalValue":      p.TotalValue,
					"initialBalance":  p.InitialBalance,
					"totalPnL":        p.TotalPnL,
					"totalPnLPercent": p.TotalPnLPercent,
					"winTrades":       p.WinTrades,
					"lossTrades":      p.LossTrades,
					"totalTrades":     p.TotalTrades,
				}
			},
		)
		if err := telegramBot.Start(); err != nil {
			logger.Error("Failed to start Telegram bot", "error", err)
		} else {
			defer telegramBot.Stop()
			logger.Info("Telegram bot integration enabled")
		}
	}

	// Create HTTP handlers (services already implement the handler interfaces)
	orderHandler := httpadapter.NewOrderHandler(orderService)
	botHandler := httpadapter.NewBotHandler(botService)
	portfolioHandler := httpadapter.NewPortfolioHandler(portfolioService)
	tradeHistoryHandler := httpadapter.NewTradeHistoryHandler(tradeHistoryService)
	healthHandler := httpadapter.NewHealthHandler()
	exchangeHandler := httpadapter.NewExchangeHandler(exchangeManager)
	settingsHandler := httpadapter.NewSettingsHandler(cfg, prefsRepo)
	tradingService := service.NewTradingService()
	tradingHandler := httpadapter.NewTradingHandler(tradingService, botService)
	newsHandler := httpadapter.NewNewsHandler()
	// Accounts persist in the users table (migration 008); sessions stay in Redis.
	authHandler := httpadapter.NewAuthHandlerWithUsers(redisAdapter, database.NewUserRepository(db.Pool))
	dcaHandler := httpadapter.NewDCABotHandler(dcaService, authHandler)
	notificationHandler := httpadapter.NewNotificationHandler(db.Pool)
	performanceHandler := httpadapter.NewPerformanceHandler(tradeHistoryService)
	journalHandler := httpadapter.NewJournalHandler(db.Pool)
	sltpHandler := httpadapter.NewSLTPHandler()

	// Initialize Finance handler
	financeHandler := httpadapter.NewFinanceHandler(
		db.Pool,
		financeAccountService,
		financeTransactionService,
		financeCategoryService,
		financeBudgetService,
		financeGoalService,
		financeAssetService,
		financeLiabilityService,
		financeSubscriptionService,
		financeDiaryService,
		dashboardService,
		financeCalculatorService,
		netWorthService,
		authHandler,
	)

	// Setup router
	router := httpadapter.NewRouter(authHandler)
	// The strategy service calls the Service routes (route_access.go) with the
	// same shared AUTH_TOKEN the backend uses to call it.
	router.SetServiceToken(os.Getenv("AUTH_TOKEN"))
	router.RegisterOrderRoutes(orderHandler)
	router.RegisterBotRoutes(botHandler)
	router.RegisterPortfolioRoutes(portfolioHandler)
	router.RegisterTradeHistoryRoutes(tradeHistoryHandler)
	router.RegisterHealthRoute(healthHandler)
	router.RegisterExchangeRoutes(exchangeHandler)
	router.RegisterSettingsRoutes(settingsHandler)
	router.RegisterTradingRoutes(tradingHandler)
	router.RegisterNewsRoutes(newsHandler)
	router.RegisterAuthRoutes(authHandler)
	router.RegisterNotificationRoutes(notificationHandler)
	router.RegisterPerformanceRoutes(performanceHandler)
	router.RegisterJournalRoutes(journalHandler)
	router.RegisterSLTPRoutes(sltpHandler)
	router.RegisterFinanceRoutes(financeHandler)

	// Register new feature routes (paper trading, risk, alerts, backtest, metrics)
	featureHandler.RegisterRoutes(router.Mux())

	// Register real trade routes
	tradeHandler := httpadapter.NewTradeHandler(exchangeManager, db.Pool)
	tradeHandler.RegisterRoutes(router.Mux())

	// Register DCA bot routes
	dcaHandler.RegisterRoutes(router.Mux())

	// Copy Trading
	copyTradingService := service.NewCopyTradingService(db.Pool)
	copyTradingHandler := httpadapter.NewCopyTradingHandler(copyTradingService, authHandler)
	copyTradingHandler.RegisterRoutes(router.Mux())

	// Portfolio Rebalancing
	rebalancingService := service.NewRebalancingService(db.Pool)
	rebalancingHandler := httpadapter.NewRebalancingHandler(rebalancingService, authHandler)
	rebalancingHandler.RegisterRoutes(router.Mux())

	// DEX/AMM HTTP routes (dexManager already initialized above)
	dexService := service.NewDexService(dexManager, walletService, db.Pool, &cfg.Dex)
	dexHandler := httpadapter.NewDexHandler(dexService, walletService, authHandler)
	dexHandler.RegisterRoutes(router.Mux())
	logger.Info("DEX/AMM trading initialized", "enabled", cfg.Dex.Enabled, "default_dex", cfg.Dex.DefaultDEX)

	// Money Dashboard (aggregates PnL from all bots)
	moneyHandler := httpadapter.NewMoneyDashboardHandler(redisAdapter, paperEngine)
	moneyHandler.RegisterRoutes(router.Mux())
	logger.Info("Money dashboard endpoint registered at /api/dashboard/money")

	// Strategy service proxy: the browser calls /strategy-api/* with its
	// session token; the backend validates it and forwards to the strategy
	// service with the server-side AUTH_TOKEN (never shipped to the browser).
	strategyUpstream := os.Getenv("STRATEGY_URL")
	if strategyUpstream == "" {
		strategyUpstream = "http://strategy:8000"
	}
	strategyTimeout := httpadapter.DefaultStrategyProxyTimeout
	if v := os.Getenv("STRATEGY_PROXY_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			strategyTimeout = d
		} else {
			logger.Warn("Invalid STRATEGY_PROXY_TIMEOUT, using default", "default", strategyTimeout.String())
		}
	}
	strategyToken := os.Getenv("AUTH_TOKEN")
	strategyProxy, err := httpadapter.NewStrategyProxy(httpadapter.StrategyProxyConfig{
		UpstreamURL:  strategyUpstream,
		ServiceToken: strategyToken,
		Validate:     authHandler.ValidateToken,
		IsAdmin:      authHandler.IsAdmin,
		Timeout:      strategyTimeout,
	})
	if err != nil {
		logger.Error("Strategy proxy disabled", "error", err)
	} else {
		router.Mux().Handle(httpadapter.StrategyProxyPrefix+"/", strategyProxy)
		if strategyToken == "" {
			logger.Warn("AUTH_TOKEN is empty: strategy proxy forwards without a service token")
		}
		logger.Info("Strategy proxy registered", "prefix", httpadapter.StrategyProxyPrefix, "timeout", strategyTimeout.String())
	}

	// Wrap with audit middleware
	auditMiddleware := httpadapter.NewAuditMiddleware(auditService, authHandler)
	handler := auditMiddleware.Middleware(router)

	// Wrap with security headers middleware
	handler = httpadapter.SecurityHeadersMiddleware(handler)

	// Wrap with body size limiter (1MB max)
	handler = httpadapter.BodyLimitMiddleware(handler, 1<<20)

	// Create HTTP server
	httpServer := &http.Server{
		Addr:         ":" + cfg.Server.Port,
		Handler:      handler,
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
	}

	// Create WebSocket server (with auth validation)
	wsHandler := websocket.NewWSHandler(wsBroadcaster, authHandler.ValidateToken)
	wsServer := &http.Server{
		Addr: ":" + cfg.WebSocket.Port,
	}
	http.HandleFunc("/ws", wsHandler.HandleWebSocket)

	// Connect to Binance WebSocket
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := binanceStream.Connect(ctx); err != nil {
		logger.Info("Binance WebSocket not connected (will retry on demand)", "error", err)
	} else {
		// Start streaming for default symbols
		go func() {
			time.Sleep(2 * time.Second) // Wait for connection to stabilize
			if err := marketDataService.StartStreaming(ctx, model.BTCUSDT); err != nil {
				logger.Info("BTCUSDT streaming not started", "error", err)
			}
			if err := marketDataService.StartStreaming(ctx, model.ETHUSDT); err != nil {
				logger.Info("ETHUSDT streaming not started", "error", err)
			}
		}()
	}

	// Fallback price poller: REST-based polling to feed strategy service when WS is unavailable
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		symbols := []string{"BTCUSDT", "ETHUSDT", "BTCTHB", "ETHTHB"}

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				for _, symbol := range symbols {
					tickerInfo, err := exchangeManager.GetTicker(ctx, symbol)
					if err != nil {
						logger.Info("Price poller: failed to fetch ticker", "symbol", symbol, "error", err)
						continue
					}
					marketData := &model.MarketData{
						Symbol:    model.TradeSymbol(symbol),
						Price:     tickerInfo.LastPrice,
						Volume:    tickerInfo.Volume,
						Timestamp: time.Now(),
					}
					// Publish to Redis so strategy service can analyze
					if err := redisAdapter.PublishMarketData(ctx, marketData); err != nil {
						logger.Info("Price poller: failed to publish to Redis", "symbol", symbol, "error", err)
					}
					// Feed paper engine + price alert monitor
					paperEngine.UpdatePrice(symbol, tickerInfo.LastPrice)
					priceAlertMonitor.CheckPrice(symbol, tickerInfo.LastPrice)
				}
			}
		}
	}()

	// Start HTTP/HTTPS server in goroutine
	go func() {
		if cfg.Server.TLSCertFile != "" && cfg.Server.TLSKeyFile != "" {
			logger.Info("Starting HTTPS server", "port", cfg.Server.Port, "tls", "enabled")
			if err := httpServer.ListenAndServeTLS(cfg.Server.TLSCertFile, cfg.Server.TLSKeyFile); err != nil && err != http.ErrServerClosed {
				logger.Fatal("HTTPS server error", "error", err)
			}
		} else {
			logger.Info("Starting HTTP server (TLS not configured)", "port", cfg.Server.Port)
			if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				logger.Fatal("HTTP server error", "error", err)
			}
		}
	}()

	// Start WebSocket server in goroutine
	go func() {
		if cfg.Server.TLSCertFile != "" && cfg.Server.TLSKeyFile != "" {
			logger.Info("Starting WSS server (TLS not auto-configured for WS — use reverse proxy)", "port", cfg.WebSocket.Port)
		} else {
			logger.Info("Starting WebSocket server", "port", cfg.WebSocket.Port)
		}
		if err := wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("WebSocket server error", "error", err)
		}
	}()

	// Start gRPC server
	if err := grpcServer.Start(); err != nil {
		logger.Error("Failed to start gRPC server", "error", err)
	}

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down servers")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("HTTP server shutdown error", "error", err)
	}

	if err := wsServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("WebSocket server shutdown error", "error", err)
	}

	// Stop gRPC server
	grpcServer.Stop()

	// Shutdown market data service
	marketDataService.Shutdown()

	if err := redisAdapter.Close(); err != nil {
		logger.Error("Redis connection close error", "error", err)
	}

	logger.Info("Servers stopped")
}
