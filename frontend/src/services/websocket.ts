/**
 * WebSocket service for real-time data streaming.
 */
import { MarketData, BotStatus, Order, WebSocketMessage, TradeNotification, BotActivity } from '@/types';
import { getAuthToken } from './auth-headers';

export type WebSocketMessageHandler = (message: WebSocketMessage) => void;

export class WebSocketService {
  private ws: WebSocket | null = null;
  private url: string;
  private reconnectAttempts = 0;
  private readonly baseReconnectDelay = 1000;   // 1 s initial
  private readonly maxReconnectDelay = 30000;   // 30 s ceiling
  private messageHandlers: Set<WebSocketMessageHandler> = new Set();
  private reconnectTimer: NodeJS.Timeout | null = null;
  /** Set to true when disconnect() is called intentionally; prevents auto-reconnect. */
  private intentionalClose = false;

  constructor(url?: string) {
    this.url = url || process.env.NEXT_PUBLIC_WS_URL || 'ws://localhost:8081/ws';
  }

  connect(): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      return;
    }

    // The backend only streams to authenticated sockets. The token goes in
    // the first message, never the URL (URLs end up in proxy access logs).
    const token = getAuthToken();
    if (!token) {
      return;
    }

    this.intentionalClose = false;

    try {
      this.ws = new WebSocket(this.url);

      this.ws.onopen = () => {
        this.ws?.send(JSON.stringify({ type: 'auth', token }));
        console.log('WebSocket connected');
        this.reconnectAttempts = 0;
      };

      this.ws.onmessage = (event) => {
        try {
          const message: WebSocketMessage = JSON.parse(event.data);
          if ((message.type as string) === 'auth_ok') {
            return;
          }
          this.messageHandlers.forEach((handler) => handler(message));
        } catch (error) {
          console.error('Failed to parse WebSocket message:', error);
        }
      };

      this.ws.onclose = () => {
        if (!this.intentionalClose) {
          this.scheduleReconnect();
        }
      };

      this.ws.onerror = () => {
        // onerror is always followed by onclose; reconnect is handled there.
      };
    } catch (error) {
      console.error('Failed to connect WebSocket:', error);
      this.scheduleReconnect();
    }
  }

  disconnect(): void {
    this.intentionalClose = true;

    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }

    if (this.ws) {
      this.ws.close();
      this.ws = null;
    }
    this.reconnectAttempts = 0;
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer) return; // already scheduled

    // Exponential backoff with jitter: delay = min(base * 2^attempt, max) ± 10%
    const expDelay = this.baseReconnectDelay * Math.pow(2, this.reconnectAttempts);
    const cappedDelay = Math.min(expDelay, this.maxReconnectDelay);
    const jitter = cappedDelay * 0.1 * (Math.random() * 2 - 1);
    const delay = Math.round(cappedDelay + jitter);

    this.reconnectAttempts++;
    console.log(`WebSocket: reconnecting in ${delay}ms (attempt ${this.reconnectAttempts})`);

    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, delay);
  }

  onMessage(handler: WebSocketMessageHandler): () => void {
    this.messageHandlers.add(handler);

    // Return unsubscribe function
    return () => {
      this.messageHandlers.delete(handler);
    };
  }

  onMarketData(handler: (data: MarketData) => void): () => void {
    return this.onMessage((message) => {
      if (message.type === 'market_data') {
        handler(message.data as MarketData);
      }
    });
  }

  onBotStatus(handler: (status: BotStatus) => void): () => void {
    return this.onMessage((message) => {
      if (message.type === 'bot_status') {
        handler(message.data as BotStatus);
      }
    });
  }

  onOrderUpdate(handler: (order: Order) => void): () => void {
    return this.onMessage((message) => {
      if (message.type === 'order_update') {
        handler(message.data as Order);
      }
    });
  }

  onTradeNotification(handler: (trade: TradeNotification) => void): () => void {
    return this.onMessage((message) => {
      if (message.type === 'trade_notification') {
        handler(message.data as TradeNotification);
      }
    });
  }

  onBotActivity(handler: (activity: BotActivity) => void): () => void {
    return this.onMessage((message) => {
      if (message.type === 'bot_activity') {
        handler(message.data as BotActivity);
      }
    });
  }

  sendPing(): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: 'ping' }));
    }
  }

  subscribe(symbol: string): void {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: 'subscribe', symbol }));
    }
  }

  isConnected(): boolean {
    return this.ws?.readyState === WebSocket.OPEN;
  }
}

// Export singleton instance
export const wsService = new WebSocketService();
