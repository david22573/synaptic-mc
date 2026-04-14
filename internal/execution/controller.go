package execution

import (
	"context"

	"david22573/synaptic-mc/internal/domain"
)

// Controller handles the boundary between the Go engine and the TS bot.
// It is strictly responsible for dispatching commands and signaling control states.
type Controller interface {
	Dispatch(ctx context.Context, action domain.Action) error
	Preload(ctx context.Context, action domain.Action) error
	AbortCurrent(ctx context.Context, reason string) error
	Close() error
	IsReady() bool
}
