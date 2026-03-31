package orchestrator

import (
	"context"
	"log/slog"

	"david22573/synaptic-mc/internal/domain"
)

// IngestControlInput satisfies the observability.ControlOrchestrator interface.
// It receives high-frequency UI inputs (like mouse-look) and routes them to the active controller.
func (o *Orchestrator) IngestControlInput(ctx context.Context, input domain.ControlInput) {
	// Throttle logging so we don't spam stdout at 20 FPS
	if input.Action != "camera_move" {
		o.logger.Debug("Received control input from UI", slog.String("action", input.Action))
	}

	o.mu.RLock()
	cm := o.ctrlManager
	o.mu.RUnlock()

	if cm == nil || !cm.HasActiveController() {
		return
	}

	// Assuming your execution engine/controller has a way to handle raw inputs.
	// For example, routing the yaw/pitch to the mineflayer bot's look function.
	// If your Controller interface doesn't have this yet, you can type-assert
	// or expand the interface to accept raw domain.ControlInput.

	/* Example implementation:
	if ctrl, ok := cm.GetActiveController().(interface {
		HandleDirectInput(domain.ControlInput)
	}); ok {
		ctrl.HandleDirectInput(input)
	}
	*/
}
