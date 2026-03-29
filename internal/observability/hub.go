package observability

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
)

const (
	writeWait      = 10 * time.Second
	maxMessageSize = 512 * 1024 // 512KB max message size
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Lock this down in production via ENV vars
	},
}

type UIClient struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
}

// Hub manages WebSocket connections for the external UI dashboard.
type Hub struct {
	clients    map[*UIClient]bool
	broadcast  chan []byte
	register   chan *UIClient
	unregister chan *UIClient
	logger     *slog.Logger
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		broadcast:  make(chan []byte, 1024), // Buffered to prevent blocking the caller
		register:   make(chan *UIClient),
		unregister: make(chan *UIClient),
		clients:    make(map[*UIClient]bool),
		logger:     logger.With(slog.String("component", "ui_hub")),
	}
}

func (h *Hub) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			h.logger.Info("UI Hub shutting down")
			// Disconnect all clients cleanly
			for client := range h.clients {
				close(client.send)
				delete(h.clients, client)
			}
			return
		case client := <-h.register:
			h.clients[client] = true
			h.logger.Info("UI Client connected", slog.Int("active_clients", len(h.clients)))
		case client := <-h.unregister:
			if _, ok := h.clients[client]; ok {
				delete(h.clients, client)
				close(client.send)
				h.logger.Info("UI Client disconnected", slog.Int("active_clients", len(h.clients)))
			}
		case message := <-h.broadcast:
			for client := range h.clients {
				select {
				case client.send <- message:
				default:
					// CRITICAL: If the client's send buffer is full, drop the client.
					// A slow UI must never block the engine's event loop.
					h.logger.Warn("UI Client buffer full, dropping connection")
					close(client.send)
					delete(h.clients, client)
				}
			}
		}
	}
}

// Broadcast serializes and sends a message to all connected UI clients asynchronously.
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
	for {
		// We don't expect the UI to send much, but we must read to process ping/pong/close frames
		if _, _, err := c.conn.ReadMessage(); err != nil {
			break
		}
	}
}

func (c *UIClient) writePump() {
	defer c.conn.Close()

	for message := range c.send {
		if err := c.conn.SetWriteDeadline(time.Now().Add(writeWait)); err != nil {
			return
		}
		if err := c.conn.WriteMessage(websocket.TextMessage, message); err != nil {
			return
		}
	}

	// If the channel was closed, send a graceful close message
	c.conn.SetWriteDeadline(time.Now().Add(writeWait))
	c.conn.WriteMessage(websocket.CloseMessage, []byte{})
}
