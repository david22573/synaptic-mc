package domain

import (
	"context"
	"log"
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

// LocalEventBus provides a bounded, asynchronous event bus with a drop policy.
// It prevents slow subscribers or event bursts from stalling the entire system.
type LocalEventBus struct {
	mu          sync.RWMutex
	subscribers map[EventType][]EventHandler
	queue       chan DomainEvent
}

func NewEventBus() *LocalEventBus {
	b := &LocalEventBus{
		subscribers: make(map[EventType][]EventHandler),
		queue:       make(chan DomainEvent, 1024), // Bounded buffer
	}
	go b.dispatcher()
	return b
}

func (b *LocalEventBus) Publish(ctx context.Context, event DomainEvent) {
	select {
	case b.queue <- event:
		// Success
	default:
		// DROP: Prevents total system stall when the bus is overloaded.
		// Usually happens if downstream consumers are blocked or LLM latency is extreme.
		log.Printf("[EventBus] WARNING: Queue full, dropping event: %s", event.Type)
	}
}

func (b *LocalEventBus) Subscribe(eventType EventType, handler EventHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.subscribers[eventType] = append(b.subscribers[eventType], handler)
}

func (b *LocalEventBus) dispatcher() {
	for ev := range b.queue {
		b.mu.RLock()
		handlers := b.subscribers[ev.Type]
		b.mu.RUnlock()

		for _, h := range handlers {
			// Execute handler in its own goroutine to ensure isolation.
			// One slow subscriber cannot block the central dispatcher.
			go h.HandleEvent(context.Background(), ev)
		}
	}
}
