package websocket

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
	"trading-bot-system/backend/internal/logger"

	"trading-bot-system/backend/internal/domain/model"

	"github.com/gorilla/websocket"
)

// WebSocketBroadcasterImpl implements the WebSocketBroadcaster interface
type WebSocketBroadcasterImpl struct {
	clients    map[chan []byte]bool
	broadcast  chan []byte
	register   chan chan []byte
	unregister chan chan []byte
	mu         sync.RWMutex
	maxClients int
}

// NewWebSocketBroadcaster creates a new WebSocket broadcaster
func NewWebSocketBroadcaster(maxClients, queueSize int) *WebSocketBroadcasterImpl {
	return &WebSocketBroadcasterImpl{
		clients:    make(map[chan []byte]bool),
		broadcast:  make(chan []byte, queueSize),
		register:   make(chan chan []byte),
		unregister: make(chan chan []byte),
		maxClients: maxClients,
	}
}

// Run starts the broadcaster goroutine
func (w *WebSocketBroadcasterImpl) Run() {
	for {
		select {
		case client := <-w.register:
			w.mu.Lock()
			if len(w.clients) < w.maxClients {
				w.clients[client] = true
				logger.Info("WebSocket client connected", "total_clients", len(w.clients))
			} else {
				close(client)
				logger.Info("WebSocket client rejected - max clients reached")
			}
			w.mu.Unlock()

		case client := <-w.unregister:
			w.mu.Lock()
			if _, ok := w.clients[client]; ok {
				delete(w.clients, client)
				close(client)
				logger.Info("WebSocket client disconnected", "total_clients", len(w.clients))
			}
			w.mu.Unlock()

		case message := <-w.broadcast:
			w.mu.RLock()
			for client := range w.clients {
				select {
				case client <- message:
				default:
					// Client buffer full, mark for removal
					go func(c chan []byte) {
						w.unregister <- c
					}(client)
				}
			}
			w.mu.RUnlock()
		}
	}
}

// BroadcastMarketData broadcasts market data to all connected clients
func (w *WebSocketBroadcasterImpl) BroadcastMarketData(data *model.MarketData) {
	message := map[string]interface{}{
		"type": "market_data",
		"data": data,
	}
	w.broadcastMessage(message)
}

// BroadcastBotStatus broadcasts bot status to all connected clients
func (w *WebSocketBroadcasterImpl) BroadcastBotStatus(status *model.BotStatus) {
	message := map[string]interface{}{
		"type": "bot_status",
		"data": status,
	}
	w.broadcastMessage(message)
}

// BroadcastOrderUpdate broadcasts order updates to all connected clients
func (w *WebSocketBroadcasterImpl) BroadcastOrderUpdate(order *model.Order) {
	message := map[string]interface{}{
		"type": "order_update",
		"data": order,
	}
	w.broadcastMessage(message)
}

// BroadcastTradeNotification broadcasts a trade execution notification
func (w *WebSocketBroadcasterImpl) BroadcastTradeNotification(trade *model.TradeNotification) {
	message := map[string]interface{}{
		"type": "trade_notification",
		"data": trade,
	}
	w.broadcastMessage(message)
}

// BroadcastBotActivity broadcasts bot activity/status updates
func (w *WebSocketBroadcasterImpl) BroadcastBotActivity(activity *model.BotActivity) {
	message := map[string]interface{}{
		"type": "bot_activity",
		"data": activity,
	}
	w.broadcastMessage(message)
}

// RegisterClient registers a new WebSocket client
func (w *WebSocketBroadcasterImpl) RegisterClient(ch chan []byte) {
	w.register <- ch
}

// UnregisterClient unregisters a WebSocket client
func (w *WebSocketBroadcasterImpl) UnregisterClient(ch chan []byte) {
	w.unregister <- ch
}

func (w *WebSocketBroadcasterImpl) broadcastMessage(message map[string]interface{}) {
	jsonData, err := json.Marshal(message)
	if err != nil {
		logger.Error("Failed to marshal WebSocket message", "error", err)
		return
	}

	select {
	case w.broadcast <- jsonData:
	default:
		logger.Info("Broadcast channel full, dropping message")
	}
}

// checkOrigin validates the WebSocket origin against allowed origins from FRONTEND_URL env var
func checkOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false // No origin header means it's likely a non-browser client
	}

	allowedURLs := getEnv("FRONTEND_URL", "http://localhost:3000")
	allowedOrigins := strings.Split(allowedURLs, ",")

	// Also allow all in development if FRONTEND_URL is set to "*"
	if allowedURLs == "*" {
		logger.Warn("WebSocket allows all origins (FRONTEND_URL=*)")
		return true
	}

	for _, allowed := range allowedOrigins {
		if strings.TrimSpace(allowed) == origin {
			return true
		}
	}

	logger.Info("WebSocket origin rejected", "origin", origin)
	return false
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}

// wsAuthTimeout bounds how long an unauthenticated socket may stay open
// waiting for its {"type":"auth"} message.
var wsAuthTimeout = 10 * time.Second

// wsReadLimit caps client messages (auth, ping, subscribe are all tiny) so an
// unauthenticated socket cannot make the server buffer a large frame.
const wsReadLimit = 4096

// Upgrader for WebSocket connections with origin validation
var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin:     checkOrigin,
}

// WSHandler handles WebSocket connections
type WSHandler struct {
	broadcaster   *WebSocketBroadcasterImpl
	authValidator func(token string) (string, bool) // validates auth token, returns userID
}

// NewWSHandler creates a new WebSocket handler
func NewWSHandler(broadcaster *WebSocketBroadcasterImpl, authValidator func(token string) (string, bool)) *WSHandler {
	return &WSHandler{
		broadcaster:   broadcaster,
		authValidator: authValidator,
	}
}

// HandleWebSocket handles WebSocket upgrade and connection.
//
// The session token never travels in the URL (URLs land in proxy access
// logs). Non-browser clients send "Authorization: Bearer <token>" on the
// upgrade request; browsers, which cannot set that header, connect and send
// {"type":"auth","token":"<token>"} as their first message within
// wsAuthTimeout. Nothing is broadcast to a socket until it authenticates.
func (h *WSHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Has("token") {
		http.Error(w, `{"error":"Do not put the token in the URL. Send Authorization: Bearer <token>, or {\"type\":\"auth\",\"token\":...} as the first message"}`, http.StatusBadRequest)
		return
	}
	if h.authValidator == nil {
		http.Error(w, `{"error":"Authentication unavailable"}`, http.StatusServiceUnavailable)
		return
	}

	headerToken := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		headerToken = strings.TrimPrefix(auth, "Bearer ")
		if _, valid := h.authValidator(headerToken); !valid {
			http.Error(w, `{"error":"Invalid or expired authentication token"}`, http.StatusUnauthorized)
			return
		}
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		logger.Error("Failed to upgrade WebSocket connection", "error", err)
		return
	}

	conn.SetReadLimit(wsReadLimit)

	if headerToken == "" && !h.authenticateFirstMessage(conn) {
		conn.Close()
		return
	}

	clientChan := make(chan []byte, 100)
	h.broadcaster.RegisterClient(clientChan)

	// Ensure client is unregistered on disconnect
	defer func() {
		h.broadcaster.UnregisterClient(clientChan)
		conn.Close()
	}()

	// Handle incoming messages (ping/pong, commands, etc.)
	go h.handleMessages(conn)

	// Send messages to client
	for message := range clientChan {
		if err := conn.WriteMessage(websocket.TextMessage, message); err != nil {
			logger.Error("Failed to send WebSocket message", "error", err)
			return
		}
	}
}

// authenticateFirstMessage reads the {"type":"auth","token":...} message and
// replies {"type":"auth_ok"}, or closes with a policy violation.
func (h *WSHandler) authenticateFirstMessage(conn *websocket.Conn) bool {
	conn.SetReadDeadline(time.Now().Add(wsAuthTimeout))
	var msg struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	err := conn.ReadJSON(&msg)
	conn.SetReadDeadline(time.Time{})
	if err == nil && msg.Type == "auth" && msg.Token != "" {
		if _, valid := h.authValidator(msg.Token); valid {
			return conn.WriteJSON(map[string]string{"type": "auth_ok"}) == nil
		}
	}
	conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "authentication required"),
		time.Now().Add(time.Second))
	return false
}

func (h *WSHandler) handleMessages(conn *websocket.Conn) {
	for {
		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				logger.Error("WebSocket error", "error", err)
			}
			break
		}

		// Process incoming message
		var msg map[string]interface{}
		if err := json.Unmarshal(message, &msg); err != nil {
			continue
		}

		// Handle different message types
		msgType, ok := msg["type"].(string)
		if !ok {
			continue
		}

		switch msgType {
		case "ping":
			// Respond with pong
			response := map[string]string{"type": "pong"}
			jsonData, _ := json.Marshal(response)
			conn.WriteMessage(websocket.TextMessage, jsonData)
		case "subscribe":
			// Handle subscription requests
			logger.Info("WebSocket subscription request", "data", msg)
		}
	}
}

// GetClientCount returns the number of connected clients
func (w *WebSocketBroadcasterImpl) GetClientCount() int {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return len(w.clients)
}
