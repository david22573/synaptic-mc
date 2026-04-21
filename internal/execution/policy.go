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

type Strategy string

const (
	StrategyRetrySame      Strategy = "retry_same"
	StrategyRetryDifferent Strategy = "retry_different"
	StrategyDegrade        Strategy = "degrade"
	StrategyAbort          Strategy = "abort"
)

type FailureDirective struct {
	Strategy Strategy
	Delay    time.Duration
	Fallback string
}

func EvaluateFailure(res domain.ExecutionResult, attempts int) FailureDirective {
	delay := time.Duration(attempts) * 2 * time.Second
	if attempts > 3 {
		return FailureDirective{Strategy: StrategyAbort}
	}
	class := ClassifyFailure(res.Cause)
	switch class {
	case FailureRecoverable:
		return FailureDirective{Strategy: StrategyRetrySame, Delay: delay}
	case FailureEnvironmental:
		return FailureDirective{Strategy: StrategyRetryDifferent, Delay: delay}
	case FailureFatal:
		return FailureDirective{Strategy: StrategyAbort}
	case FailurePreempted:
		return FailureDirective{Strategy: StrategyAbort}
	default:
		return FailureDirective{Strategy: StrategyRetrySame, Delay: delay}
	}
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
