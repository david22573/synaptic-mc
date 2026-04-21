package execution

import (
	"context"
	"log/slog"
	"time"

	"github.com/anthdm/hollywood/actor"
	"github.com/gorilla/websocket"

	"david22573/synaptic-mc/internal/domain"
)

type wsDispatchMsg struct{ ctx context.Context; action domain.Action }
type wsPreloadMsg struct{ ctx context.Context; action domain.Action }
type wsAbortMsg struct{ ctx context.Context; reason string }
type wsCloseMsg struct{}
type wsPingMsg struct{}

type wsActor struct {
	conn      *websocket.Conn
	logger    *slog.Logger
	sessionID string
	onMessage func(msgType string, payload []byte)
	onClose   func()
	
	seen     map[string]time.Time
	keys     []string
	capacity int
	ttl      time.Duration
}

func NewWSActor(conn *websocket.Conn, logger *slog.Logger, sessionID string, onMsg func(string, []byte), onClose func()) actor.Producer {
	return func() actor.Receiver {
		return &wsActor{
			conn:      conn,
			logger:    logger,
			sessionID: sessionID,
			onMessage: onMsg,
			onClose:   onClose,
			seen:      make(map[string]time.Time),
			keys:      make([]string, 0, 1000),
			capacity:  1000,
			ttl:       10 * time.Minute,
		}
	}
}

func (a *wsActor) Receive(ctx *actor.Context) {
	switch msg := ctx.Message().(type) {
	case actor.Started:
		a.logger.Info("WS Actor started")
		go a.readPump(ctx.PID(), ctx.Engine())

		// Fire a ping into the mailbox every 10 seconds
		ticker := time.NewTicker(10 * time.Second)
		go func(pid *actor.PID, eng *actor.Engine, t *time.Ticker) {
			for range t.C {
				eng.Send(pid, wsPingMsg{})
			}
		}(ctx.PID(), ctx.Engine(), ticker)

	case actor.Stopped:
		a.logger.Info("WS Actor stopped")
		_ = a.conn.Close()
		if a.onClose != nil {
			a.onClose()
		}

	case wsPingMsg:
		// Standard WebSocket ping. If it fails, the connection is dead, so poison the actor.
		if err := a.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second)); err != nil {
			ctx.Engine().Poison(ctx.PID())
		}

	case wsDispatchMsg:
		if a.isDuplicate(msg.action.ID) {
			return
		}
		a.recordSeen(msg.action.ID)
		
		payload := map[string]interface{}{
			"type": "dispatch",
			"data": msg.action,
		}
		_ = a.conn.WriteJSON(payload)

	case wsPreloadMsg:
		if a.isDuplicate(msg.action.ID) {
			return
		}
		payload := map[string]interface{}{
			"type": "preload",
			"data": msg.action,
		}
		_ = a.conn.WriteJSON(payload)

	case wsAbortMsg:
		payload := map[string]interface{}{
			"type": "abort",
			"data": map[string]string{"reason": msg.reason},
		}
		_ = a.conn.WriteJSON(payload)

	case wsCloseMsg:
		ctx.Engine().Poison(ctx.PID())
	}
}

func (a *wsActor) isDuplicate(id string) bool {
	a.cleanupExpired()
	if ts, exists := a.seen[id]; exists {
		if time.Since(ts) < a.ttl {
			return true
		}
	}
	return false
}

func (a *wsActor) recordSeen(id string) {
	if len(a.keys) >= a.capacity {
		oldest := a.keys[0]
		a.keys = a.keys[1:]
		delete(a.seen, oldest)
	}
	a.seen[id] = time.Now()
	a.keys = append(a.keys, id)
}

func (a *wsActor) cleanupExpired() {
	now := time.Now()
	for len(a.keys) > 0 {
		oldestID := a.keys[0]
		if ts, ok := a.seen[oldestID]; ok && now.Sub(ts) > a.ttl {
			delete(a.seen, oldestID)
			a.keys = a.keys[1:]
		} else {
			break
		}
	}
}

func (a *wsActor) readPump(pid *actor.PID, engine *actor.Engine) {
	defer engine.Poison(pid)
	
	for {
		var raw map[string]interface{}
		err := a.conn.ReadJSON(&raw)
		if err != nil {
			return
		}
		
		msgType, _ := raw["type"].(string)
		payloadBytes := domain.MustMarshal(raw["data"])
		
		if a.onMessage != nil {
			a.onMessage(msgType, payloadBytes)
		}
	}
}
