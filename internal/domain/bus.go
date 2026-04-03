package domain

import (
	"context"
	"sync"
)

// EventHandler defines how components react to domain events.
type EventHandler interface {
	HandleEvent(ctx context.Context, event DomainEvent)
}

// FuncHandler allows using a plain function as an EventHandler.
type FuncHandler func(ctx context.Context, event DomainEvent)

func (f FuncHandler) HandleEvent(ctx context.Context, event DomainEvent) {
	f(ctx, event)
}

// EventBus is the central nervous system for decoupled services.
type EventBus interface {
	Publish(ctx context.Context, event DomainEvent)
	Subscribe(eventType EventType, handler EventHandler)
}

// LocalEventBus provides a simple, thread-safe, in-memory event bus.
type LocalEventBus struct {
	mu       sync.RWMutex
	handlers map[EventType][]EventHandler
}

func NewEventBus() *LocalEventBus {
	return &LocalEventBus{
		handlers: make(map[EventType][]EventHandler),
	}
}

func (b *LocalEventBus) Publish(ctx context.Context, event DomainEvent) {
	b.mu.RLock()
	handlers := b.handlers[event.Type]
	b.mu.RUnlock()

	for _, h := range handlers {
		h.HandleEvent(ctx, event)
	}
}

func (b *LocalEventBus) Subscribe(eventType EventType, handler EventHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.handlers[eventType] = append(b.handlers[eventType], handler)
}
