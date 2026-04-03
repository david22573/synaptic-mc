package execution

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type FailureRecord struct {
	IntentID    string
	Count       int
	LastFailure time.Time
}

type RecoveryLevel int

const (
	RecoveryJump RecoveryLevel = iota
	RecoveryStrafe
	RecoveryRepath
	RecoveryPanicTeleport
)

type StabilityState struct {
	ReflexActive bool
	IsStuck      bool
}

// IngestControlInput satisfies the observability.ControlOrchestrator interface.
// It receives high-frequency UI inputs (like mouse-look) and routes them directly
// to the active controller, bypassing the standard task execution queue.
func (s *ControlService) IngestControlInput(ctx context.Context, input domain.ControlInput) {
	// Throttle logging so we don't spam stdout at high tick rates
	if input.Action != "camera_move" {
		s.logger.Debug("Received control input from UI", slog.String("action", input.Action))
	}

	if s.ctrlManager == nil || !s.ctrlManager.HasActiveController() {
		return
	}

	// Pack the raw control data (Yaw/Pitch) into a JSON string so it can
	// survive transit through the standard domain.Action schema.
	targetData := map[string]float64{
		"yaw":   input.Yaw,
		"pitch": input.Pitch,
	}

	payloadBytes, err := json.Marshal(targetData)
	if err != nil {
		s.logger.Error("Failed to marshal control input target data", slog.Any("error", err))
		return
	}

	// Wrap the control input into a domain.Action
	action := domain.Action{
		ID:        fmt.Sprintf("ctrl-%d", time.Now().UnixNano()),
		Source:    "ui_direct_control",
		Action:    input.Action,
		Target:    domain.Target{Type: "direct_input", Name: string(payloadBytes)},
		Priority:  1000, // Absolute highest priority
		Rationale: "Direct user control input",
	}

	// Dispatch directly to the active controller.
	// This skips the TaskExecutionEngine queue entirely and goes straight to the WSController.
	err = s.ctrlManager.Dispatch(ctx, action)

	if err != nil && input.Action != "camera_move" {
		s.logger.Error("Failed to dispatch control input", slog.Any("error", err))
	}
}
