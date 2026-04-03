package domain

import "strings"

// NormalizeEventType ensures all inbound event types are canonical.
func NormalizeEventType(s string) EventType {
	switch strings.ToUpper(strings.TrimSpace(s)) {

	case "TASK_START", "TASKSTART":
		return EventTypeTaskStart

	case "TASK_END", "TASKEND":
		return EventTypeTaskEnd

	case "STATE_UPDATED", "STATEUPDATE":
		return EventTypeStateUpdated

	case "STATE_TICK", "STATETICK":
		return EventTypeStateTick

	case "PLAN_CREATED":
		return EventTypePlanCreated

	case "PLAN_INVALIDATED":
		return EventTypePlanInvalidated

	case "PLAN_COMPLETED":
		return EventTypePlanCompleted

	case "PLAN_FAILED":
		return EventTypePlanFailed

	case "BOT_DEATH":
		return EventBotDeath

	case "BOT_RESPAWN":
		return EventBotRespawn

	case "PANIC":
		return EventTypePanic

	case "PANIC_RESOLVED":
		return EventTypePanicResolved

	// NEW: recognise UI control events
	case "CONTROL_INPUT":
		return EventType("CONTROL_INPUT")

	case "CAMERA_MOVE":
		return EventType("CAMERA_MOVE")

	default:
		return EventType("UNKNOWN")
	}
}
