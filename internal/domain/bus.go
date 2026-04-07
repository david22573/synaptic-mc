package domain

import (
	"context"
	"log"
	"sync"
	"time"
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

type subscriber struct {
	ch      chan DomainEvent
	handler EventHandler
}

// LocalEventBus provides a simple, thread-safe, in-memory event bus.
// It uses internal buffering and async dispatch to prevent slow subscribers
// from deadlocking the event loop.
type LocalEventBus struct {
	mu       sync.RWMutex
	handlers map[EventType][]*subscriber
}

const publishTimeout = 100 * time.Millisecond

func NewEventBus() *LocalEventBus {
	return &LocalEventBus{
		handlers: make(map[EventType][]*subscriber),
	}
}

func (b *LocalEventBus) Publish(ctx context.Context, event DomainEvent) {
	b.mu.RLock()
	subs := b.handlers[event.Type]
	b.mu.RUnlock()

	for _, sub := range subs {
		timer := time.NewTimer(publishTimeout)
		select {
		case sub.ch <- event:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			// Keep delivery bounded so a stalled subscriber cannot wedge the bus,
			// while still allowing brief bursts to drain without losing control-flow events.
			log.Printf("[EventBus] CRITICAL: Dropping event %s for backed up subscriber. Buffer full.", event.Type)
		}
	}
}

func (b *LocalEventBus) Subscribe(eventType EventType, handler EventHandler) {
	b.mu.Lock()
	defer b.mu.Unlock()

	sub := &subscriber{
		ch:      make(chan DomainEvent, 1024), // Large buffer to handle bursts
		handler: handler,
	}

	b.handlers[eventType] = append(b.handlers[eventType], sub)

	// Start a dedicated worker for this subscriber to process its queue asynchronously
	go func() {
		// Subscribers run in their own lifecycle.
		// Using Background since the original Publish context is transient.
		for ev := range sub.ch {
			sub.handler.HandleEvent(context.Background(), ev)
		}
	}()
}
