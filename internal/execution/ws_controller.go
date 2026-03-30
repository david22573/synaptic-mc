package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"

	"github.com/gorilla/websocket"
)

// WSController implements Controller for WebSocket-connected agents.
// It handles wire serialization and thread-safe socket writes.
type WSController struct {
	conn    *websocket.Conn
	writeMu sync.Mutex
}

func NewWSController(conn *websocket.Conn) *WSController {
	return &WSController{
		conn: conn,
	}
}

func (c *WSController) Dispatch(ctx context.Context, action domain.Action) error {
	payload, err := json.Marshal(map[string]any{
		"id":        action.ID,
		"action":    action.Action,
		"target":    action.Target,
		"count":     action.Count,
		"rationale": action.Rationale,
	})
	if err != nil {
		return fmt.Errorf("failed to marshal action: %w", err)
	}

	msg := map[string]any{
		"type":    "command",
		"trace":   action.Trace,
		"payload": json.RawMessage(payload),
	}

	return c.writeJSON(ctx, msg)
}

func (c *WSController) AbortCurrent(ctx context.Context, reason string) error {
	payload, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return fmt.Errorf("failed to marshal abort payload: %w", err)
	}

	msg := map[string]any{
		"type":    "abort_task",
		"payload": json.RawMessage(payload),
	}

	return c.writeJSON(ctx, msg)
}

func (c *WSController) Close() error {
	return c.conn.Close()
}

func (c *WSController) writeJSON(ctx context.Context, v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	// Honor context cancellation or fallback to a hard 5-second deadline
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(5 * time.Second)
	}

	if err := c.conn.SetWriteDeadline(deadline); err != nil {
		return err
	}

	return c.conn.WriteJSON(v)
}
