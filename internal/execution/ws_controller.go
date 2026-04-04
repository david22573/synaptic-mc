package execution

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"david22573/synaptic-mc/internal/domain"
)

const (
	writeWait      = 10 * time.Second
	pongWait       = 15 * time.Second
	pingPeriod     = (pongWait * 9) / 10
	maxMessageSize = 512000
)

type WSMessage struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type WSController struct {
	conn      *websocket.Conn
	logger    *slog.Logger
	sendCh    chan WSMessage
	onMessage func(msgType string, payload []byte)
	onClose   func()
	closeOnce sync.Once
	mu        sync.Mutex
	isClosed  bool
}

func NewWSController(
	conn *websocket.Conn,
	logger *slog.Logger,
	onMessage func(msgType string, payload []byte),
	onClose func(),
) *WSController {
	c := &WSController{
		conn:      conn,
		logger:    logger.With(slog.String("component", "ws_controller")),
		sendCh:    make(chan WSMessage, 256),
		onMessage: onMessage,
		onClose:   onClose,
	}

	c.conn.SetReadLimit(maxMessageSize)

	go c.readPump()
	go c.writePump()

	return c
}

func (c *WSController) IsReady() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.isClosed
}

func (c *WSController) Dispatch(ctx context.Context, action domain.Action) error {
	c.mu.Lock()
	if c.isClosed {
		c.mu.Unlock()
		return errors.New("websocket closed")
	}
	c.mu.Unlock()

	// Phase 3 Fix #8: Trace log the flow
	c.logger.Info("Dispatching action",
		slog.String("action", action.Action),
		slog.String("target_name", action.Target.Name),
		slog.String("target_type", action.Target.Type),
		slog.String("task_id", action.ID),
	)

	payload, err := json.Marshal(action)
	if err != nil {
		return err
	}

	msg := WSMessage{
		Type:    "DISPATCH_TASK",
		Payload: payload,
	}

	// Phase 3 Fix #7: Drop-with-warn on full channel instead of returning error
	select {
	case c.sendCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	default:
		c.logger.Warn("Send channel full, dropping dispatch message to avoid blocking", slog.String("action", action.Action))
		return nil
	}
}

func (c *WSController) AbortCurrent(ctx context.Context, reason string) error {
	payload, _ := json.Marshal(map[string]string{"reason": reason})

	msg := WSMessage{
		Type:    "ABORT_TASK",
		Payload: payload,
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isClosed {
		return errors.New("websocket closed")
	}

	// Phase 3 Fix #7: Apply the same drop-with-warn logic to the abort channel push
	select {
	case c.sendCh <- msg:
		return nil
	default:
		c.logger.Warn("Send channel full, dropping abort message to avoid blocking", slog.String("reason", reason))
		return nil
	}
}

func (c *WSController) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.isClosed = true
		c.mu.Unlock()

		c.conn.Close()
		if c.onClose != nil {
			c.onClose()
		}
	})
	return nil
}

func (c *WSController) readPump() {
	defer c.Close()

	_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
	c.conn.SetPongHandler(func(string) error {
		_ = c.conn.SetReadDeadline(time.Now().Add(pongWait))
		return nil
	})

	for {
		_, msgData, err := c.conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.logger.Error("Unexpected websocket close", slog.Any("error", err))
			} else {
				c.logger.Info("Websocket read pump closed gracefully")
			}
			break
		}

		var msg WSMessage
		if err := json.Unmarshal(msgData, &msg); err != nil {
			c.logger.Warn("Failed to unmarshal websocket message", slog.Any("error", err))
			continue
		}

		// Phase 1/5: Parse and log the ExecutionResult directly for observability
		if msg.Type == "TASK_END" {
			var res domain.ExecutionResult
			if err := json.Unmarshal(msg.Payload, &res); err == nil {
				c.logger.Info("Execution result parsed",
					slog.Bool("success", res.Success),
					slog.String("cause", res.Cause),
					slog.Float64("progress", res.Progress),
				)
			}
		}

		// Route to manager/engine
		if c.onMessage != nil {
			c.onMessage(msg.Type, msg.Payload)
		}
	}
}

func (c *WSController) writePump() {
	ticker := time.NewTicker(pingPeriod)
	defer func() {
		ticker.Stop()
		c.Close()
	}()

	for {
		select {
		case msg, ok := <-c.sendCh:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if !ok {
				_ = c.conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}

			if err := c.conn.WriteJSON(msg); err != nil {
				c.logger.Error("Failed to write JSON to websocket", slog.Any("error", err))
				return
			}

		case <-ticker.C:
			_ = c.conn.SetWriteDeadline(time.Now().Add(writeWait))
			if err := c.conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}
