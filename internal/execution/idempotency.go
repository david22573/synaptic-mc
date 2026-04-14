package execution

import (
	"context"
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

const defaultIDTTL = 10 * time.Minute

type seenEntry struct {
	id        string
	timestamp time.Time
}

// IdempotentController wraps an Execution Controller to drop duplicate commands.
// Uses a combination of capacity-based FIFO and TTL-based eviction to prevent memory leaks.
type IdempotentController struct {
	base     Controller
	capacity int
	ttl      time.Duration

	mu   sync.Mutex
	seen map[string]time.Time
	keys []seenEntry
}

func NewIdempotentController(base Controller, capacity int) *IdempotentController {
	return &IdempotentController{
		base:     base,
		capacity: capacity,
		ttl:      defaultIDTTL,
		seen:     make(map[string]time.Time, capacity),
		keys:     make([]seenEntry, 0, capacity),
	}
}

func (c *IdempotentController) IsReady() bool {
	return c.base.IsReady()
}

func (c *IdempotentController) Dispatch(ctx context.Context, action domain.Action) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	c.mu.Lock()

	// 1. Periodic TTL Cleanup (Internal)
	c.cleanupExpired()

	// 2. Check if seen and not expired
	if ts, exists := c.seen[action.ID]; exists {
		if time.Since(ts) < c.ttl {
			c.mu.Unlock()
			return nil // Silently drop, already dispatched within TTL
		}
		// If expired, we'll fall through and treat it as new (though cleanup likely got it)
	}

	// 3. Capacity Enforcement (FIFO)
	if len(c.keys) >= c.capacity {
		oldest := c.keys[0]
		c.keys = c.keys[1:]
		delete(c.seen, oldest.id)
	}

	// 4. Record new action
	now := time.Now()
	c.seen[action.ID] = now
	c.keys = append(c.keys, seenEntry{id: action.ID, timestamp: now})
	c.mu.Unlock()

	return c.base.Dispatch(ctx, action)
}

func (c *IdempotentController) Preload(ctx context.Context, action domain.Action) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}

	c.mu.Lock()
	c.cleanupExpired()

	if ts, exists := c.seen[action.ID]; exists {
		if time.Since(ts) < c.ttl {
			c.mu.Unlock()
			return nil
		}
	}
	c.mu.Unlock()

	return c.base.Preload(ctx, action)
}

func (c *IdempotentController) cleanupExpired() {
	now := time.Now()
	// Since keys is sorted by time (FIFO), we can just check from the front
	for len(c.keys) > 0 && now.Sub(c.keys[0].timestamp) > c.ttl {
		delete(c.seen, c.keys[0].id)
		c.keys = c.keys[1:]
	}
}

func (c *IdempotentController) Clear(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.seen, id)

	// Clean out the key from history so capacity doesn't silently shrink
	for i, k := range c.keys {
		if k.id == id {
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
