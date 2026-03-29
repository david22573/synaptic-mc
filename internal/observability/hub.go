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
		return true // Relaxed for local agent UI development
	},
}

type UIClient struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	mu   sync.Mutex
}

type Hub struct {
	clients    map[*UIClient]bool
	broadcast  chan []byte
	register   chan *UIClient
	unregister chan *UIClient
	logger     *slog.Logger

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
			h.pingAll()

		case client := <-h.register:
			h.mu.Lock()
			if !h.isShutdown {
				h.clients[client] = true
				h.logger.Info("UI Client connected", slog.Int("active_clients", len(h.clients)))
			} else {
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

			for _, client := range clients {
				select {
				case client.send <- message:
				default:
					h.logger.Warn("UI Client buffer full, scheduling drop")
					go func(c *UIClient) {
						// Non-blocking unregister to prevent deadlocks during teardown
						select {
						case h.unregister <- c:
						default:
						}
					}(client)
				}
			}
		}
	}
}

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
				// Non-blocking unregister
				select {
				case h.unregister <- c:
				default:
				}
			}(client)
		}
		client.mu.Unlock()
	}
}

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

	// DO NOT close channels (register, unregister, broadcast) here.
	// Lingering readPump/writePump goroutines will panic if they send to closed channels.
	// Since the Hub loop is dead, we rely on GC and non-blocking sends to clean up gracefully.
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
		return ws.SetReadDeadline(time.Now().Add(pongWait))
	})

	client := &UIClient{hub: h, conn: ws, send: make(chan []byte, 256)}

	// Non-blocking register check
	select {
	case h.register <- client:
	default:
		ws.Close()
		return
	}

	go client.writePump()
	go client.readPump()
}

func (c *UIClient) readPump() {
	defer func() {
		// Non-blocking unregister prevents deadlock/panic when Hub shuts down
		select {
		case c.hub.unregister <- c:
		default:
		}
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	for {
		_, _, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.hub.logger.Warn("UI Client read error", slog.Any("error", err))
			}
			break
		}
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
			c.mu.Lock()
			if err := c.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(writeWait)); err != nil {
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
		}
	}
}
