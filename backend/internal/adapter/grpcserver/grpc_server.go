package grpcserver

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"
	"trading-bot-system/backend/internal/adapter/dex"
	"trading-bot-system/backend/internal/logger"

	"trading-bot-system/backend/internal/domain/model"
	"trading-bot-system/backend/internal/port/input"
	"trading-bot-system/backend/internal/port/output/pb"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// GRPCServer implements the gRPC server for inter-service communication
type GRPCServer struct {
	pb.UnimplementedOrderExecutionServiceServer
	pb.UnimplementedBotStatusServiceServer
	pb.UnimplementedMarketDataServiceServer
	pb.UnimplementedDexServiceServer

	orderHandler      input.OrderHandler
	botHandler        input.BotHandler
	marketDataHandler input.MarketDataHandler
	dexGRPC           *dexGRPCServer

	server *grpc.Server
	port   string
	mu     sync.Mutex

	// serviceToken is the shared AUTH_TOKEN every call must present as
	// "authorization: Bearer <token>" metadata. Start refuses to serve
	// without one.
	serviceToken string

	// Market data subscribers
	subscribers map[chan *pb.MarketData]bool
	subMu       sync.RWMutex
}

// NewGRPCServer creates a new gRPC server
func NewGRPCServer(
	port string,
	orderHandler input.OrderHandler,
	botHandler input.BotHandler,
	marketDataHandler input.MarketDataHandler,
	dexManager *dex.DEXManager,
) *GRPCServer {
	return &GRPCServer{
		port:              port,
		orderHandler:      orderHandler,
		botHandler:        botHandler,
		marketDataHandler: marketDataHandler,
		dexGRPC:           newDexGRPCServer(dexManager),
		subscribers:       make(map[chan *pb.MarketData]bool),
	}
}

// SetServiceToken sets the bearer token callers must send.
func (s *GRPCServer) SetServiceToken(token string) {
	s.serviceToken = token
}

// Start starts the gRPC server
func (s *GRPCServer) Start() error {
	if s.serviceToken == "" {
		return fmt.Errorf("gRPC service token is empty: set AUTH_TOKEN; refusing to serve unauthenticated order execution")
	}

	lis, err := net.Listen("tcp", fmt.Sprintf(":%s", s.port))
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}

	s.server = grpc.NewServer(
		grpc.UnaryInterceptor(s.unaryInterceptor),
		grpc.StreamInterceptor(s.streamInterceptor),
	)

	// Register services
	pb.RegisterOrderExecutionServiceServer(s.server, s)
	pb.RegisterBotStatusServiceServer(s.server, s)
	pb.RegisterMarketDataServiceServer(s.server, s)
	pb.RegisterDexServiceServer(s.server, s.dexGRPC)

	logger.Info("Starting gRPC server", "port", s.port)

	go func() {
		if err := s.server.Serve(lis); err != nil {
			logger.Error("Failed to serve gRPC server", "error", err)
		}
	}()

	return nil
}

// Stop stops the gRPC server
func (s *GRPCServer) Stop() {
	if s.server != nil {
		s.server.GracefulStop()
	}
}

// ============ Order Execution Service ============

// ExecuteOrder executes a trading order from the strategy service
func (s *GRPCServer) ExecuteOrder(ctx context.Context, req *pb.OrderRequest) (*pb.OrderResponse, error) {
	logger.Info("Received order execution request", "symbol", req.Symbol, "side", req.Side, "quantity", req.Quantity)

	// Convert protobuf request to domain model
	orderReq := &model.OrderRequest{
		Symbol:   model.TradeSymbol(req.Symbol),
		Side:     model.OrderSide(req.Side),
		Quantity: req.Quantity,
		Price:    req.Price,
	}

	// Execute order through handler
	response, err := s.orderHandler.CreateOrder(ctx, orderReq)
	if err != nil {
		logger.Error("Order execution failed", "error", err)
		return &pb.OrderResponse{
			Status: "REJECTED",
			Error:  err.Error(),
		}, status.Errorf(codes.Internal, "order execution failed: %v", err)
	}

	logger.Info("Order executed successfully", "order_id", response.OrderID)

	return &pb.OrderResponse{
		OrderId:   response.OrderID,
		Symbol:    string(response.Symbol),
		Side:      string(response.Side),
		Status:    string(response.Status),
		Quantity:  response.Quantity,
		Price:     0, // OrderResponse doesn't have Price field
		CreatedAt: response.CreatedAt.UnixMilli(),
	}, nil
}

// CancelOrder cancels an order
func (s *GRPCServer) CancelOrder(ctx context.Context, req *pb.CancelOrderRequest) (*pb.CancelOrderResponse, error) {
	logger.Info("Received cancel order request", "order_id", req.OrderId)

	err := s.orderHandler.CancelOrder(ctx, req.OrderId)
	if err != nil {
		return &pb.CancelOrderResponse{
			Success: false,
			Message: err.Error(),
		}, status.Errorf(codes.Internal, "cancel order failed: %v", err)
	}

	return &pb.CancelOrderResponse{
		Success: true,
		Message: "Order cancelled successfully",
	}, nil
}

// ============ Bot Status Service ============

// GetBotStatus returns the current bot status
func (s *GRPCServer) GetBotStatus(ctx context.Context, req *pb.GetBotStatusRequest) (*pb.BotStatusResponse, error) {
	botStatus, err := s.botHandler.GetStatus(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get bot status: %v", err)
	}

	var startedAt, stoppedAt int64
	if !botStatus.StartedAt.IsZero() {
		startedAt = botStatus.StartedAt.UnixMilli()
	}
	if !botStatus.StoppedAt.IsZero() {
		stoppedAt = botStatus.StoppedAt.UnixMilli()
	}

	return &pb.BotStatusResponse{
		IsActive:    botStatus.IsActive,
		StartedAt:   startedAt,
		StoppedAt:   stoppedAt,
		TotalTrades: int32(botStatus.TotalTrades),
		TotalProfit: botStatus.TotalProfit,
	}, nil
}

// StartBot starts the trading bot
func (s *GRPCServer) StartBot(ctx context.Context, req *pb.StartBotRequest) (*pb.StartBotResponse, error) {
	logger.Info("Received start bot request")

	// gRPC starts bot in signal mode (no grid params)
	err := s.botHandler.Start(ctx, nil)
	if err != nil {
		return &pb.StartBotResponse{
			Success: false,
			Message: err.Error(),
		}, status.Errorf(codes.Internal, "failed to start bot: %v", err)
	}

	return &pb.StartBotResponse{
		Success: true,
		Message: "Bot started successfully",
	}, nil
}

// StopBot stops the trading bot
func (s *GRPCServer) StopBot(ctx context.Context, req *pb.StopBotRequest) (*pb.StopBotResponse, error) {
	logger.Info("Received stop bot request")

	err := s.botHandler.Stop(ctx)
	if err != nil {
		return &pb.StopBotResponse{
			Success: false,
			Message: err.Error(),
		}, status.Errorf(codes.Internal, "failed to stop bot: %v", err)
	}

	return &pb.StopBotResponse{
		Success: true,
		Message: "Bot stopped successfully",
	}, nil
}

// ============ Market Data Service ============

// SubscribeMarketData streams market data to subscribers
func (s *GRPCServer) SubscribeMarketData(req *pb.SubscribeRequest, stream pb.MarketDataService_SubscribeMarketDataServer) error {
	logger.Info("New market data subscription", "symbols", req.Symbols)

	// Create channel for this subscriber
	ch := make(chan *pb.MarketData, 100)

	s.subMu.Lock()
	s.subscribers[ch] = true
	s.subMu.Unlock()

	defer func() {
		s.subMu.Lock()
		delete(s.subscribers, ch)
		s.subMu.Unlock()
		close(ch)
	}()

	// Send market data to client
	for {
		select {
		case <-stream.Context().Done():
			logger.Info("Market data stream closed by client")
			return nil
		case data, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(data); err != nil {
				logger.Error("Failed to send market data via gRPC stream", "error", err)
				return err
			}
		}
	}
}

// BroadcastMarketData broadcasts market data to all gRPC subscribers
func (s *GRPCServer) BroadcastMarketData(data *model.MarketData) {
	s.subMu.RLock()
	defer s.subMu.RUnlock()

	pbData := &pb.MarketData{
		Symbol:    string(data.Symbol),
		Price:     data.Price,
		Volume:    data.Volume,
		Timestamp: data.Timestamp.UnixMilli(),
	}

	for ch := range s.subscribers {
		select {
		case ch <- pbData:
		default:
			// Channel full, skip this subscriber
			logger.Info("Warning: gRPC market data channel full")
		}
	}
}

// ============ Interceptors ============

func (s *GRPCServer) unaryInterceptor(
	ctx context.Context,
	req interface{},
	info *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (interface{}, error) {
	if err := s.authorize(ctx); err != nil {
		logger.Warn("gRPC call rejected", "method", info.FullMethod)
		return nil, err
	}
	start := time.Now()

	resp, err := handler(ctx, req)

	duration := time.Since(start)
	logger.Info("gRPC unary request completed", "method", info.FullMethod, "duration", duration)

	return resp, err
}

func (s *GRPCServer) streamInterceptor(
	srv interface{},
	ss grpc.ServerStream,
	info *grpc.StreamServerInfo,
	handler grpc.StreamHandler,
) error {
	if err := s.authorize(ss.Context()); err != nil {
		logger.Warn("gRPC stream rejected", "method", info.FullMethod)
		return err
	}
	start := time.Now()

	err := handler(srv, ss)

	duration := time.Since(start)
	logger.Info("gRPC stream request completed", "method", info.FullMethod, "duration", duration)

	return err
}

// authorize checks the bearer token in the incoming metadata.
func (s *GRPCServer) authorize(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	for _, v := range md.Get("authorization") {
		token, ok := strings.CutPrefix(v, "Bearer ")
		if ok && s.serviceToken != "" &&
			subtle.ConstantTimeCompare([]byte(token), []byte(s.serviceToken)) == 1 {
			return nil
		}
	}
	return status.Error(codes.Unauthenticated, "missing or invalid service token")
}
