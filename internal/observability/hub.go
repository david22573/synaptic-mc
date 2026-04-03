package observability

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"

	"github.com/gorilla/websocket"
)

const (
	writeWait           = 10 * time.Second
	pongWait            = 15 * time.Second // Synced with WSController timeout tolerance
	pingPeriod          = (pongWait * 9) / 10
	maxMessageSize      = 512 * 1024
	MsgTypeControlInput = "CONTROL_INPUT"
	MsgTypeStateSync    = "STATE_SYNC"
	MsgTypeFailureLog   = "FAILURE_LOG"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true // Relaxed for local agent UI development
	},
}

type StructuredFailureLog struct {
	PlanID        string `json:"plan_id"`
	Reason        string `json:"reason"`
	StateSnapshot string `json:"state_snapshot"`
	FailureCount  int    `json:"failure_count"`
}

type ControlOrchestrator interface {
	IngestControlInput(ctx context.Context, input domain.ControlInput)
}

type UIClient struct {
	hub  *Hub
	conn *websocket.Conn
	send chan []byte
	mu   sync.Mutex
}

type Hub struct {
	clients      map[*UIClient]bool
	broadcast    chan []byte
	register     chan *UIClient
	unregister   chan *UIClient
	logger       *slog.Logger
	orchestrator ControlOrchestrator

	mu         sync.RWMutex
	isShutdown bool
}

func NewHub(logger *slog.Logger) *Hub {
	return &Hub{
		broadcast:  make(chan []byte, 1024),
		register:   make(chan *UIClient, 32),
		unregister: make(chan *UIClient, 32),
		clients:    make(map[*UIClient]bool),
		logger:     logger.With(slog.String("component", "ui_hub")),
	}
}

func (h *Hub) SetOrchestrator(orch ControlOrchestrator) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.orchestrator = orch
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

	for client := range h.clients {
		client.mu.Lock()
		_ = client.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "server shutting down"))
		client.mu.Unlock()

		client.conn.Close()
		delete(h.clients, client)
	}
}

func (h *Hub) Broadcast(msgType string, payload any) {
	// ---- produce the exact JSON the UI expects ----
	env := struct {
		Type    string `json:"type"`
		Payload any    `json:"payload"`
	}{
		Type:    msgType,
		Payload: payload,
	}

	raw, _ := json.Marshal(env)

	select {
	case h.broadcast <- raw:
	default:
		h.logger.Warn("UI Hub broadcast channel full, dropping message")
	}
}

func (h *Hub) BroadcastFailureLog(failure StructuredFailureLog) {
	h.logger.Error("Structured Failure Log",
		slog.String("plan_id", failure.PlanID),
		slog.String("reason", failure.Reason),
		slog.Int("failure_count", failure.FailureCount),
	)
	h.Broadcast(MsgTypeFailureLog, failure)
}

func (h *Hub) handleControlInput(client *UIClient, payload []byte) {
	var input domain.ControlInput
	if err := json.Unmarshal(payload, &input); err != nil {
		h.logger.Error("Invalid control input", slog.Any("error", err))
		return
	}

	h.mu.RLock()
	orch := h.orchestrator
	h.mu.RUnlock()

	if orch != nil {
		orch.IngestControlInput(context.Background(), input)
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
		select {
		case c.hub.unregister <- c:
		default:
			c.hub.logger.Warn("Unregister channel full, dropping client forcefully")
		}
		c.conn.Close()
	}()

	c.conn.SetReadDeadline(time.Now().Add(pongWait))
	for {
		_, msg, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.hub.logger.Warn("UI Client read error", slog.Any("error", err))
			}
			break
		}

		var envelope struct {
			Type    string          `json:"type"`
			Payload json.RawMessage `json:"payload"`
		}

		if err := json.Unmarshal(msg, &envelope); err == nil {
			if envelope.Type == MsgTypeControlInput {
				c.hub.handleControlInput(c, envelope.Payload)
			}
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
