package execution

import (
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type FailureClass string

const (
	FailureRecoverable FailureClass = "recoverable"
	FailureEnvironmental FailureClass = "environmental"
	FailureFatal         FailureClass = "fatal"
	FailurePreempted     FailureClass = "preempted"
)

type RetryPolicy struct {
	MaxRetries        int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	Multiplier        float64
}

var DefaultRetryPolicy = RetryPolicy{
	MaxRetries:     5,
	InitialBackoff: 1 * time.Second,
	MaxBackoff:     30 * time.Second,
	Multiplier:     2.0,
}

func ClassifyFailure(cause string) FailureClass {
	switch cause {
	case "timeout", "network_error":
		return FailureRecoverable
	case "blocked", "no_path", string(domain.CauseStuckTerrain):
		return FailureEnvironmental
	case "panic", "nil_state", "death":
		return FailureFatal
	case "preempted", "emergency_interrupt":
		return FailurePreempted
	default:
		return FailureRecoverable
	}
}

func GetPriority(action string) int {
	switch action {
	case "retreat", "emergency_reflex":
		return 100 // Tier 1: Survival
	case "hunt", "defend":
		return 80  // Tier 2: Tactical
	case "gather", "mine", "craft":
		return 50  // Tier 3: Normal
	default:
		return 10  // Tier 4: Background
	}
}
