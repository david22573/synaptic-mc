package domain

import (
	"context"

	"github.com/anthdm/hollywood/actor"
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

// ActorEventBus wraps Hollywood's native event stream to satisfy EventBus interface.
type ActorEventBus struct {
	engine *actor.Engine
}

func NewActorEventBus(engine *actor.Engine) *ActorEventBus {
	return &ActorEventBus{
		engine: engine,
	}
}

// Publish drops the event directly into Hollywood's high-performance stream.
func (b *ActorEventBus) Publish(ctx context.Context, ev DomainEvent) {
	b.engine.BroadcastEvent(ev)
}

// Subscribe supports legacy components that still expect a standard callback.
func (b *ActorEventBus) Subscribe(eventType EventType, handler EventHandler) {
	// Hollywood v1 does not expose the raw subscription list easily for func-based subs
	// without an actor. We spawn a proxy actor for legacy func handlers.
	b.engine.SpawnFunc(func(ctx *actor.Context) {
		switch msg := ctx.Message().(type) {
		case actor.Started:
			ctx.Engine().Subscribe(ctx.PID())
		case DomainEvent:
			if msg.Type == eventType || eventType == "" {
				handler.HandleEvent(context.Background(), msg)
			}
		}
	}, "event_sub_proxy")
}

// SubscribeActor allows new actors to route events directly into their mailboxes.
func (b *ActorEventBus) SubscribeActor(pid *actor.PID) {
	b.engine.Subscribe(pid)
}
