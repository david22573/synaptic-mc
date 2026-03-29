package observability

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 60 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512 * 1024
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		// FIX: Implement proper origin checking for production
		origin := r.Header.Get("Origin")
		return origin == "http://localhost:8080" || origin == "http://127.0.0.1:8080"
	},
}

type UIClient struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	// FIX: Add mutex to protect send channel operations
	mu sync.Mutex
}

type Hub struct {
	clients    map[*UIClient]bool
	broadcast  chan []byte
	register   chan *UIClient
	unregister chan *UIClient
	logger     *slog.Logger

	// FIX: Add shutdown tracking
	mu         sync.RWMutex
	isShutdown bool
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		broadcast:  make(chan []byte, 1024),
		register:   make(chan *UIClient),
		unregister: make(chan *UIClient),
		clients:    make(map[*UIClient]bool),
		logger:     logger.With(slog.String("component", "ui_hub")),
	}
}

func (h *Hub) Run(ctx context.Context) {
	ticker := time.NewTicker(pingPeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			h.logger.Info("UI Hub shutting down")
			h.shutdown()
			return

		case <-ticker.C:
			// FIX: Send pings to all clients periodically
			h.pingAll()

		case client := <-h.register:
			h.mu.Lock()
			if !h.isShutdown {
				h.clients[client] = true
				h.logger.Info("UI Client connected", slog.Int("active_clients", len(h.clients)))
			} else {
				// FIX: Don't register new clients during shutdown
				client.conn.Close()
			}
			h.mu.Unlock()

		case client := <-h.unregister:
			h.mu.Lock()
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				h.logger.Info("UI Client disconnected", slog.Int("active_clients", len(h.clients)))
			}
			h.mu.Unlock()

		case message := <-h.broadcast:
			h.mu.RLock()
			clients := make([]*UIClient, 0, len(h.clients))
			for client := range h.clients {
				clients = append(clients, client)
			}
			h.mu.RUnlock()

			// FIX: Broadcast to clients without blocking the loop
			for _, client := range clients {
				select {
				case client.send <- message:
				default:
					// FIX: Non-blocking send with client drop
					h.logger.Warn("UI Client buffer full, scheduling drop")
					go func(c *UIClient) {
						h.unregister <- c
					}(client)
				}
			}
		}
	}
}

// FIX: New method for periodic pings
func (h *Hub) pingAll() {
	h.mu.RLock()
	clients := make([]*UIClient, 0, len(h.clients))
	for client := range h.clients {
		clients = append(clients, client)
	}
	h.mu.RUnlock()

	for _, client := range clients {
		client.mu.Lock()
		if err := client.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
			h.logger.Warn("Failed to ping client, will unregister", slog.Any("error", err))
			go func(c *UIClient) {
				h.unregister <- c
			}(client)
		}
		client.mu.Unlock()
	}
}

// FIX: Graceful shutdown method
func (h *Hub) shutdown() {
	h.mu.Lock()
	defer h.mu.Unlock()

	if h.isShutdown {
		return
	}
	h.isShutdown = true

	// Close all client connections
	for client := range h.clients {
		client.conn.Close()
		delete(h.clients, client)
	}

	// Drain channels to prevent goroutine leaks
	close(h.register)
	close(h.unregister)

	// Give broadcast some time to drain, then close it
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(h.broadcast)
	}()
}

func (h *Hub) Broadcast(msgType string, payload any) {
	msg := map[string]any{
		"type":    msgType,
		"payload": payload,
	}

	data, err := json.Marshal(msg)
	if err != nil {
		h.logger.Error("Failed to marshal broadcast message", slog.Any("error", err))
		return
	}

	// FIX: Non-blocking broadcast with drop
	select {
	case h.broadcast <- data:
	default:
		h.logger.Warn("UI Hub broadcast channel full, dropping message")
	}
}

func (h *Hub) HandleConnections(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		h.logger.Error("UI Hub upgrade failed", slog.Any("error", err))
		return
	}

	ws.SetReadLimit(maxMessageSize)
	ws.SetPongHandler(func(string) error {
		// FIX: Update read deadline on pong
		return ws.SetReadDeadline(time.Now().Add(pongWait))
	})

	client := &UIClient{hub: h, conn: ws, send: make(chan []byte, 256)}
	h.register <- client

	go client.writePump()
	go client.readPump()
}

func (c *UIClient) readPump() {
	defer func() {
		c.hub.unregister <- c
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	for {
		// FIX: Use proper message reading with error handling
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.hub.logger.Warn("UI Client read error", slog.Any("error", err))
			}
			break
		}
		// FIX: Reset read deadline after each message
		c.conn.SetReadDeadline(time.Now().Add(pongWait))
	}
}

func (c *UIClient) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.send:
			c.mu.Lock()
			if !ok {
				// FIX: Send close message when channel is closed
				c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				c.mu.Unlock()
				return
			}

			c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()

		case <-ticker.C:
			// FIX: Send periodic pings
			c.mu.Lock()
			if err := c.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
		}
	}
}
