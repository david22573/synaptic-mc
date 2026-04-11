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

// subscriber wraps a handler with its own dedicated queue to ensure ordering and isolation.
type subscriber struct {
	handler EventHandler
	queue   chan DomainEvent
}

func (s *subscriber) run() {
	for ev := range s.queue {
		// Use Background context for now as event handlers are decoupled from the publish context.
		// However, we ensure they process events sequentially.
		s.handler.HandleEvent(context.Background(), ev)
	}
}

// LocalEventBus provides a robust, asynchronous event bus.
// It guarantees sequential delivery per subscriber and prevents critical event loss.
type LocalEventBus struct {
	mu          sync.RWMutex
	subscribers map[EventType][]*subscriber
}

func NewEventBus() *LocalEventBus {
	return &LocalEventBus{
		subscribers: make(map[EventType][]*subscriber),
	}
}

// IsCritical returns true if the event type must be delivered reliably to ensure system consistency.
func IsCritical(et EventType) bool {
	switch et {
	case EventTypeTaskStart, EventTypeTaskEnd,
		EventTypePlanCreated, EventTypePlanInvalidated,
		EventTypePlanCompleted, EventTypePlanFailed,
		EventTypePanic, EventTypePanicResolved,
		EventBotDeath, EventBotRespawn,
		EventTypeStateUpdated:
		return true
	default:
		return false
	}
}

func (b *LocalEventBus) Publish(ctx context.Context, event DomainEvent) {
	b.mu.RLock()
	subs, ok := b.subscribers[event.Type]
	b.mu.RUnlock()

	if !ok {
		return
	}

	critical := IsCritical(event.Type)

	for _, s := range subs {
		if critical {
			// For critical events, we wait for space in the subscriber's queue.
			// This provides backpressure and guarantees delivery.
			select {
			case s.queue <- event:
				// Success
			case <-ctx.Done():
				log.Printf("[EventBus] ERROR: Context cancelled while publishing critical event %s", event.Type)
			}
		} else {
			// For non-critical events (like STATE_TICK), we drop if the queue is full.
			select {
			case s.queue <- event:
				// Success
			default:
				// DROP: Prevents slow subscribers from blocking telemetry/heartbeats.
			}
		}
	}
}

func (b *LocalEventBus) Subscribe(eventType EventType, handler EventHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := &subscriber{
		handler: handler,
		queue:   make(chan DomainEvent, 1024),
	}
	
	b.subscribers[eventType] = append(b.subscribers[eventType], s)
	go s.run()
}
