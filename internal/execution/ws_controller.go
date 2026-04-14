// internal/execution/ws_controller.go
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
	ctx       context.Context
	logger    *slog.Logger
	sendCh    chan WSMessage
	onMessage func(msgType string, payload []byte)
	onClose   func()
	closeOnce sync.Once
	mu        sync.Mutex
	isClosed  bool
}

func NewWSController(
	ctx context.Context,
	conn *websocket.Conn,
	logger *slog.Logger,
	onMessage func(msgType string, payload []byte),
	onClose func(),
) *WSController {
	c := &WSController{
		ctx:       ctx,
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

	select {
	case c.sendCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return errors.New("websocket send channel timeout")
	}
}

func (c *WSController) Preload(ctx context.Context, action domain.Action) error {
	c.mu.Lock()
	if c.isClosed {
		c.mu.Unlock()
		return errors.New("websocket closed")
	}
	c.mu.Unlock()

	c.logger.Debug("Preloading action",
		slog.String("action", action.Action),
		slog.String("task_id", action.ID),
	)

	payload, err := json.Marshal(action)
	if err != nil {
		return err
	}

	msg := WSMessage{
		Type:    "TASK_PRELOAD",
		Payload: payload,
	}

	select {
	case c.sendCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return errors.New("websocket send channel timeout")
	}
}

func (c *WSController) AbortCurrent(ctx context.Context, reason string) error {
	payload, _ := json.Marshal(map[string]string{"reason": reason})

	msg := WSMessage{
		Type:    "ABORT_TASK",
		Payload: payload,
	}

	c.mu.Lock()
	if c.isClosed {
		c.mu.Unlock()
		return errors.New("websocket closed")
	}
	c.mu.Unlock()

	select {
	case c.sendCh <- msg:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(2 * time.Second):
		return errors.New("websocket send channel timeout")
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

		if msg.Type == "TASK_END" {
			var res domain.TaskEndPayload
			if err := json.Unmarshal(msg.Payload, &res); err != nil {
				c.logger.Error("Failed to unmarshal TASK_END payload", slog.Any("error", err))
			} else {
				c.logger.Info("Execution result parsed",
					slog.Bool("success", res.Success),
					slog.String("cause", res.Cause),
					slog.Float64("progress", res.Progress),
				)
			}
		}

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
		case <-c.ctx.Done():
			return

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
