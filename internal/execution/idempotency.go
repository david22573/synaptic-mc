package execution

import (
	"context"
	"david22573/synaptic-mc/internal/domain"
	"sync"
)

// IdempotentController wraps an Execution Controller to silently drop duplicate commands.
// It uses a bounded LRU cache (simplified here as a map with a manual flush) to track dispatched Task IDs.
type IdempotentController struct {
	base Controller

	mu       sync.Mutex
	seen     map[string]bool
	capacity int
}

func NewIdempotentController(base Controller, capacity int) *IdempotentController {
	return &IdempotentController{
		base:     base,
		seen:     make(map[string]bool, capacity),
		capacity: capacity,
	}
}

func (c *IdempotentController) Dispatch(ctx context.Context, action domain.Action) error {
	c.mu.Lock()
	if c.seen[action.ID] {
		c.mu.Unlock()
		return nil // Silently drop, already dispatched
	}

	// Poor man's LRU clear to prevent OOM
	if len(c.seen) >= c.capacity {
		c.seen = make(map[string]bool, c.capacity)
	}

	c.seen[action.ID] = true
	c.mu.Unlock()

	return c.base.Dispatch(ctx, action)
}

func (c *IdempotentController) AbortCurrent(ctx context.Context, reason string) error {
	return c.base.AbortCurrent(ctx, reason)
}

func (c *IdempotentController) Close() error {
	return c.base.Close()
}
