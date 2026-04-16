package domain

const (
	CausePreempted       = "preempted_by_priority"
	CausePlanInvalid     = "plan_invalidated"
	CauseSurvivalPanic   = "survival_panic"
	CausePanicTriggered  = "panic_triggered"
	CausePanic           = "panic"
	CauseUnlock          = "unlock"
	CauseBotDied         = "bot_died"
	CausePlannerWarmup   = "planner_warmup"
	CauseBlocked         = "BLOCKED"
	CauseStuck           = "STUCK"
	CauseTimeout         = "TIMEOUT"
	CauseInterrupted     = "INTERRUPTED"
	CauseMissingResource = "MISSING_RESOURCES"
	CausePartial         = "PARTIAL"
	CauseFailed          = "FAILED"
	CauseDistracted      = "DISTRACTED"
	CauseAbortedDuringHesitation = "ABORTED_DURING_HESITATION"
	CauseLagDesync      = "LAG_DESYNC"
	CauseFallingRisk    = "FALLING_RISK"
	CauseStuckTerrain   = "STUCK_TERRAIN"
	CauseBlockedMob     = "BLOCKED_MOB"
	CauseNoTool         = "NO_TOOL"
	CauseUnreachable    = "UNREACHABLE"
	CauseUnknown         = "UNKNOWN"
)

// IsControlledStop returns true if the task termination was triggered by 
// internal system logic rather than a physical failure in the game world.
func IsControlledStop(cause string) bool {
	switch cause {
	case CausePreempted, CausePlanInvalid, CauseSurvivalPanic, 
		CausePanicTriggered, CausePanic, CauseUnlock, 
		CauseBotDied, CausePlannerWarmup, CauseInterrupted:
		return true
	default:
		return false
	}
}
