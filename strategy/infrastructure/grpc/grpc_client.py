"""
gRPC client for communicating with the Go backend.
"""
import logging
import os
from typing import Optional, List, Callable

import grpc
from concurrent import futures

# Import generated protobuf files
# In production, generate with: python -m grpc_tools.protoc -I../../proto --python_out=. --grpc_python_out=. ../../proto/trading.proto
from infrastructure.grpc.trading_pb2 import (
    OrderRequest,
    OrderResponse,
    CancelOrderRequest,
    CancelOrderResponse,
    BotStatusResponse,
    GetBotStatusRequest,
    StartBotRequest,
    StartBotResponse,
    StopBotRequest,
    StopBotResponse,
    SubscribeRequest,
    MarketData,
)
from infrastructure.grpc import trading_pb2_grpc

logger = logging.getLogger(__name__)


class GRPCClient:
    """gRPC client for backend communication."""
    
    def __init__(self, host: str = "localhost", port: int = 9000):
        self.host = host
        self.port = port
        self.channel: Optional[grpc.Channel] = None
        self.order_stub: Optional[trading_pb2_grpc.OrderExecutionServiceStub] = None
        self.bot_stub: Optional[trading_pb2_grpc.BotStatusServiceStub] = None
        self.market_data_stub: Optional[trading_pb2_grpc.MarketDataServiceStub] = None
    
    @staticmethod
    def _metadata() -> list:
        """Bearer metadata the backend requires on every gRPC call."""
        token = (os.getenv("AUTH_TOKEN") or "").strip()
        return [("authorization", f"Bearer {token}")] if token else []

    def connect(self) -> bool:
        """Connect to the gRPC server."""
        if not self._metadata():
            logger.error("AUTH_TOKEN is not set: the backend rejects unauthenticated gRPC calls")
            return False
        try:
            self.channel = grpc.insecure_channel(
                f"{self.host}:{self.port}",
                options=[
                    ('grpc.keepalive_timeout_ms', 10000),
                    ('grpc.keepalive_time_ms', 30000),
                ]
            )
            
            # Test connection
            grpc.channel_ready_future(self.channel).result(timeout=5)
            
            # Initialize stubs
            self.order_stub = trading_pb2_grpc.OrderExecutionServiceStub(self.channel)
            self.bot_stub = trading_pb2_grpc.BotStatusServiceStub(self.channel)
            self.market_data_stub = trading_pb2_grpc.MarketDataServiceStub(self.channel)
            
            logger.info(f"Connected to gRPC server at {self.host}:{self.port}")
            return True
        except grpc.FutureTimeoutError:
            logger.error("Failed to connect to gRPC server: timeout")
            return False
        except Exception as e:
            logger.error(f"Failed to connect to gRPC server: {e}")
            return False
    
    def disconnect(self) -> None:
        """Disconnect from the gRPC server."""
        if self.channel:
            self.channel.close()
            logger.info("Disconnected from gRPC server")
    
    def execute_order(
        self,
        symbol: str,
        side: str,
        quantity: float,
        price: float = 0.0,
        order_type: str = "MARKET",
    ) -> Optional[OrderResponse]:
        """Execute a trading order."""
        if not self.order_stub:
            logger.error("gRPC client not connected")
            return None
        
        request = OrderRequest(
            symbol=symbol,
            side=side,
            type=order_type,
            quantity=quantity,
            price=price,
        )
        
        try:
            response = self.order_stub.ExecuteOrder(request, timeout=10, metadata=self._metadata())
            logger.info(
                f"Order executed: {response.order_id} - {response.status}"
            )
            return response
        except grpc.RpcError as e:
            logger.error(f"Order execution failed: {e.code()} - {e.details()}")
            return None
    
    def cancel_order(self, order_id: str, symbol: str) -> bool:
        """Cancel an order."""
        if not self.order_stub:
            logger.error("gRPC client not connected")
            return False
        
        request = CancelOrderRequest(
            order_id=order_id,
            symbol=symbol,
        )
        
        try:
            response = self.order_stub.CancelOrder(request, timeout=10, metadata=self._metadata())
            logger.info(f"Order cancelled: {response.message}")
            return response.success
        except grpc.RpcError as e:
            logger.error(f"Order cancellation failed: {e.code()} - {e.details()}")
            return False
    
    def get_bot_status(self) -> Optional[BotStatusResponse]:
        """Get bot status."""
        if not self.bot_stub:
            logger.error("gRPC client not connected")
            return None
        
        try:
            response = self.bot_stub.GetBotStatus(
                GetBotStatusRequest(),
                timeout=5,
                metadata=self._metadata(),
            )
            return response
        except grpc.RpcError as e:
            logger.error(f"Get bot status failed: {e.code()} - {e.details()}")
            return None
    
    def start_bot(self) -> bool:
        """Start the trading bot."""
        if not self.bot_stub:
            logger.error("gRPC client not connected")
            return False
        
        try:
            response = self.bot_stub.StartBot(StartBotRequest(), timeout=10, metadata=self._metadata())
            logger.info(f"Bot started: {response.message}")
            return response.success
        except grpc.RpcError as e:
            logger.error(f"Start bot failed: {e.code()} - {e.details()}")
            return False
    
    def stop_bot(self) -> bool:
        """Stop the trading bot."""
        if not self.bot_stub:
            logger.error("gRPC client not connected")
            return False
        
        try:
            response = self.bot_stub.StopBot(StopBotRequest(), timeout=10, metadata=self._metadata())
            logger.info(f"Bot stopped: {response.message}")
            return response.success
        except grpc.RpcError as e:
            logger.error(f"Stop bot failed: {e.code()} - {e.details()}")
            return False
    
    def subscribe_market_data(
        self,
        symbols: List[str],
        callback: Callable[[MarketData], None],
    ) -> None:
        """Subscribe to market data stream."""
        if not self.market_data_stub:
            logger.error("gRPC client not connected")
            return
        
        request = SubscribeRequest(symbols=symbols)
        
        try:
            responses = self.market_data_stub.SubscribeMarketData(request, metadata=self._metadata())
            for response in responses:
                callback(response)
        except grpc.RpcError as e:
            logger.error(f"Market data subscription failed: {e.code()} - {e.details()}")
    
    def is_connected(self) -> bool:
        """Check if connected to gRPC server."""
        if not self.channel:
            return False
        
        try:
            grpc.channel_ready_future(self.channel).result(timeout=1)
            return True
        except grpc.FutureTimeoutError:
            return False


class GRPCClientManager:
    """Manager for gRPC client with reconnection logic."""
    
    def __init__(self, host: str = "backend", port: int = 9000):
        self.host = host
        self.port = port
        self.client = GRPCClient(host, port)
        self.reconnect_attempts = 0
        self.max_reconnect_attempts = 10
        self.reconnect_delay = 5  # seconds
    
    def connect_with_retry(self) -> bool:
        """Connect with retry logic."""
        while self.reconnect_attempts < self.max_reconnect_attempts:
            if self.client.connect():
                self.reconnect_attempts = 0
                return True
            
            self.reconnect_attempts += 1
            logger.warning(
                f"Reconnection attempt {self.reconnect_attempts}/{self.max_reconnect_attempts} "
                f"in {self.reconnect_delay}s"
            )
            import time
            time.sleep(self.reconnect_delay)
        
        logger.error("Max reconnection attempts reached")
        return False
    
    def execute_order_with_retry(
        self,
        symbol: str,
        side: str,
        quantity: float,
        price: float = 0.0,
    ) -> Optional[OrderResponse]:
        """Execute order with automatic reconnection."""
        if not self.client.is_connected():
            logger.warning("gRPC client disconnected, attempting reconnect...")
            if not self.connect_with_retry():
                return None
        
        return self.client.execute_order(symbol, side, quantity, price)
