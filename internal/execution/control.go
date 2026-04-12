package execution

import (
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
	DeathCount   int
	LastDeath    time.Time
}
