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
	Action      domain.Action // Stored to allow delayed async retries
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
func (s *ControlService) IngestControlInput(ctx context.Context, input domain.ControlInput) {
	// Nuke manual camera movement entirely to prevent queue flooding
	if input.Action == "camera_move" {
		return
	}

	s.logger.Debug("Received control input from UI", slog.String("action", input.Action))

	if s.ctrlManager == nil || !s.ctrlManager.HasActiveController() {
		return
	}

	targetData := map[string]float64{
		"yaw":   input.Yaw,
		"pitch": input.Pitch,
	}

	payloadBytes, err := json.Marshal(targetData)
	if err != nil {
		s.logger.Error("Failed to marshal control input target data", slog.Any("error", err))
		return
	}

	action := domain.Action{
		ID:        fmt.Sprintf("ctrl-%d", time.Now().UnixNano()),
		Source:    "ui_direct_control",
		Action:    input.Action,
		Target:    domain.Target{Type: "direct_input", Name: string(payloadBytes)},
		Priority:  1000, // Absolute highest priority
		Rationale: "Direct user control input",
	}

	err = s.ctrlManager.Dispatch(ctx, action)

	if err != nil {
		s.logger.Error("Failed to dispatch control input", slog.Any("error", err))
	}
}
