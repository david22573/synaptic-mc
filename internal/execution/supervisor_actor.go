package execution

import (
	"context"
	"log/slog"
	"time"

	"github.com/anthdm/hollywood/actor"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/state"
)

type TaskRequestMsg struct {
	Ctx    context.Context
	Action domain.Action
}

type DangerUpdateMsg struct {
	State domain.GameState
}

// ExecutionSupervisor wraps the engine to maintain existing public API.
type ExecutionSupervisor struct {
	engine *actor.Engine
	pid    *actor.PID
}

func NewExecutionSupervisor(engine *actor.Engine, logger *slog.Logger, execEngine *TaskExecutionEngine) *ExecutionSupervisor {
	pid := engine.Spawn(func() actor.Receiver {
		return &supervisorActor{
			logger:     logger.With(slog.String("component", "supervisor_actor")),
			analyzer:   state.NewDangerAnalyzer(),
			execEngine: execEngine,
			state:      NewExecutionState(),
			policy:     DefaultRetryPolicy,
		}
	}, "execution_supervisor")

	return &ExecutionSupervisor{
		engine: engine,
		pid:    pid,
	}
}

func (s *ExecutionSupervisor) UpdateDanger(worldState domain.GameState) state.DangerState {
	s.engine.Send(s.pid, DangerUpdateMsg{State: worldState})
	return state.DangerSafe
}

func (s *ExecutionSupervisor) Request(ctx context.Context, action domain.Action) bool {
	// TaskRequestMsg is sent to the supervisor actor.
	// Since we need to return a bool (approved or not), we use Request/Result for synchronous-like behavior if needed,
	// but the snippet suggests firing a message.
	// To maintain the 'bool' return, we'll use Request.
	res, err := s.engine.Request(s.pid, TaskRequestMsg{Ctx: ctx, Action: action}, time.Second).Result()
	if err != nil {
		return false
	}
	return res.(bool)
}

func (s *ExecutionSupervisor) HandleTaskEnd(payload domain.TaskEndPayload) {
	s.engine.Send(s.pid, payload)
}

func (s *ExecutionSupervisor) GetDangerState() state.DangerState {
	res, err := s.engine.Request(s.pid, "get_danger", time.Second).Result()
	if err != nil {
		return state.DangerSafe
	}
	return res.(state.DangerState)
}

// --- Internal Actor ---
type supervisorActor struct {
	logger      *slog.Logger
	analyzer    *state.DangerAnalyzer
	dangerState state.DangerState
	execEngine  *TaskExecutionEngine
	state       *ExecutionState
	policy      RetryPolicy

	lastPanicTime time.Time
	panicCooldown time.Duration
}

func (a *supervisorActor) Receive(ctx *actor.Context) {
	switch msg := ctx.Message().(type) {
	case actor.Started:
		a.panicCooldown = 10 * time.Second

	case DangerUpdateMsg:
		newState := a.analyzer.Update(msg.State)
		if newState != a.dangerState {
			a.logger.Info("Danger state transitioned",
				slog.String("from", string(a.dangerState)),
				slog.String("to", string(newState)))
			a.dangerState = newState
		}

	case TaskRequestMsg:
		approved := a.evaluateRequest(msg.Ctx, msg.Action)
		if approved {
			// Forward to engine
			a.execEngine.ExecuteAsync(msg.Ctx, msg.Action)
		}
		ctx.Respond(approved)

	case domain.TaskEndPayload:
		a.handleTaskEnd(msg)

	case string:
		if msg == "get_danger" {
			ctx.Respond(a.dangerState)
		}
	}
}

func (a *supervisorActor) evaluateRequest(ctx context.Context, task domain.Action) bool {
	// 1. Panic Circuit Breaker
	if time.Since(a.lastPanicTime) < a.panicCooldown {
		return false
	}

	// 2. Survival Hysteresis
	if a.dangerState == state.DangerEscape && GetPriority(task.Action) < 100 {
		return false
	}

	// 3. Dedupe
	active := a.state.GetActiveTask()
	if active != nil && active.ID == task.ID {
		return true
	}

	// 4. Retry Budget
	retries, lastFail := a.state.GetRetryStats(task.Action)
	if retries > 0 {
		backoff := a.calculateBackoff(retries)
		if time.Since(lastFail) < backoff {
			return false
		}
	}
	if retries >= a.policy.MaxRetries {
		return false
	}

	// 5. Lease
	timeout := 30 * time.Second
	minHold := 0 * time.Second
	canPreempt := true

	priority := GetPriority(task.Action)
	if priority >= 100 {
		timeout = 5 * time.Second
		minHold = 2 * time.Second
		canPreempt = false
	}

	return a.state.AcquireLease(task, timeout, minHold, canPreempt)
}

func (a *supervisorActor) handleTaskEnd(payload domain.TaskEndPayload) {
	a.state.ReleaseLease(payload.CommandID)
	if payload.Status == "COMPLETED" {
		a.state.ResetRetries(payload.Action)
		return
	}

	class := ClassifyFailure(payload.Cause)
	if class == FailureFatal {
		a.state.RecordFailure(payload.Action)
		a.lastPanicTime = time.Now()
	} else if class != FailurePreempted {
		a.state.RecordFailure(payload.Action)
	}
}

func (a *supervisorActor) calculateBackoff(retries int) time.Duration {
	backoff := a.policy.InitialBackoff
	for i := 0; i < retries && i < 10; i++ {
		backoff = time.Duration(float64(backoff) * a.policy.Multiplier)
	}
	if backoff > a.policy.MaxBackoff {
		backoff = a.policy.MaxBackoff
	}
	return backoff
}
