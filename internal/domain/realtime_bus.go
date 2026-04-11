// internal/domain/realtime_bus.go
package domain

import (
	"context"
	"sync"
)

// RealtimeBus handles high-priority synchronous event dispatching for the control path.
// It bypasses bounded worker pools entirely to prevent execution deadlocks.
type RealtimeBus struct {
	subscribers map[EventType][]FuncHandler
	mu          sync.RWMutex
}

func NewRealtimeBus() *RealtimeBus {
	return &RealtimeBus{
		subscribers: make(map[EventType][]FuncHandler),
	}
}

func (b *RealtimeBus) Subscribe(eventType EventType, handler FuncHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[eventType] = append(b.subscribers[eventType], handler)
}

func (b *RealtimeBus) Publish(ctx context.Context, event DomainEvent) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if subs, ok := b.subscribers[event.Type]; ok {
		for _, sub := range subs {
			sub(ctx, event)
		}
	}
}
