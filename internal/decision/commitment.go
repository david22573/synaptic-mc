package decision

import (
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type Commitment struct {
	TaskID      string
	Objective   string
	MinDuration time.Duration
	StartTime   time.Time
}

func shouldWaitForFreshState(cause string) bool {
	switch cause {
	case domain.CauseSurvivalPanic, domain.CausePanic, domain.CausePanicTriggered, domain.CauseUnlock:
		return true
	default:
		return false
	}
}
