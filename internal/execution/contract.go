// internal/execution/contract.go
package execution

// Cause categorizes exactly why a task failed, allowing the planner to adapt.
type Cause string

const (
	CauseNone            Cause = ""
	CauseTimeout         Cause = "TIMEOUT"
	CauseBlocked         Cause = "BLOCKED"
	CauseInvalid         Cause = "INVALID"
	CauseInterrupted     Cause = "INTERRUPTED" // Usually survival reflex hijacking the bot
	CauseStuck           Cause = "STUCK"       // Physics desync / infinite pathing loops
	CauseMissingResource Cause = "MISSING_RESOURCE"
	CausePartial         Cause = "PARTIAL" // Task failed but made meaningful progress
	CauseUnknown         Cause = "UNKNOWN"
)
