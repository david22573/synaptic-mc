package execution

import (
	"context"
	"david22573/synaptic-mc/internal/domain"
	"sync"
)

// IdempotentController wraps an Execution Controller to drop duplicate commands.
// Refactored to allow clearing IDs so retries aren't blocked.
type IdempotentController struct {
	base Controller

	mu       sync.Mutex
	seen     map[string]bool
	keys     []string
	capacity int
}

func NewIdempotentController(base Controller, capacity int) *IdempotentController {
	return &IdempotentController{
		base:     base,
		seen:     make(map[string]bool, capacity),
		keys:     make([]string, 0, capacity),
		capacity: capacity,
	}
}

func (c *IdempotentController) Dispatch(ctx context.Context, action domain.Action) error {
	c.mu.Lock()
	if c.seen[action.ID] {
		c.mu.Unlock()
		return nil // Silently drop, already dispatched
	}

	if len(c.keys) >= c.capacity {
		oldest := c.keys[0]
		c.keys = c.keys[1:]
		delete(c.seen, oldest)
	}

	c.seen[action.ID] = true
	c.keys = append(c.keys, action.ID)
	c.mu.Unlock()

	return c.base.Dispatch(ctx, action)
}

func (c *IdempotentController) Clear(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.seen, id)

	// Clean out the ghost keys so capacity doesn't silently shrink
	for i, k := range c.keys {
		if k == id {
			c.keys = append(c.keys[:i], c.keys[i+1:]...)
			break
		}
	}
}

func (c *IdempotentController) AbortCurrent(ctx context.Context, reason string) error {
	return c.base.AbortCurrent(ctx, reason)
}

func (c *IdempotentController) Close() error {
	return c.base.Close()
}
