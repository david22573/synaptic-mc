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

// subscriber wraps a handler with its own dedicated queue to ensure ordering and isolation.
type subscriber struct {
	handler EventHandler
	buffer  []DomainEvent
	mu      sync.Mutex
	cond    *sync.Cond
}

func (s *subscriber) push(ev DomainEvent, critical bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// For non-critical events, we drop if the buffer is already large to prevent OOM.
	if !critical && len(s.buffer) > 1024 {
		return
	}

	s.buffer = append(s.buffer, ev)
	s.cond.Signal()
}

func (s *subscriber) run() {
	for {
		s.mu.Lock()
		for len(s.buffer) == 0 {
			s.cond.Wait()
		}
		ev := s.buffer[0]
		s.buffer = s.buffer[1:]
		s.mu.Unlock()

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
		s.push(event, critical)
	}
}

func (b *LocalEventBus) Subscribe(eventType EventType, handler EventHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()

	s := &subscriber{
		handler: handler,
		buffer:  make([]DomainEvent, 0, 10),
	}
	s.cond = sync.NewCond(&s.mu)
	
	b.subscribers[eventType] = append(b.subscribers[eventType], s)
	go s.run()
}
