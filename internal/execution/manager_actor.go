package execution

import (
	"context"
	"log/slog"
	"time"

	"github.com/anthdm/hollywood/actor"
	"github.com/gorilla/websocket"

	"david22573/synaptic-mc/internal/domain"
)

type RegisterWSConnMsg struct {
	Conn      *websocket.Conn
	SessionID string
	OnMessage func(string, []byte)
}
type GetSuccessRateMsg struct{}
type GetRecentFailuresMsg struct{}
type RecordResultMsg struct{ Result domain.ExecutionResult }
type wsClosedEventMsg struct{ PID *actor.PID }

type ControllerManager struct {
	engine *actor.Engine
	pid    *actor.PID
}

func NewControllerManagerActor(engine *actor.Engine, logger *slog.Logger) *ControllerManager {
	pid := engine.Spawn(func() actor.Receiver {
		return &managerActor{
			logger:      logger.With(slog.String("component", "manager_actor")),
			lastResults: make([]domain.ExecutionResult, 0, 20),
		}
	}, "controller_manager")

	return &ControllerManager{
		engine: engine,
		pid:    pid,
	}
}

func (m *ControllerManager) RegisterConnection(conn *websocket.Conn, sessionID string, onMsg func(string, []byte)) {
	m.engine.Send(m.pid, RegisterWSConnMsg{Conn: conn, SessionID: sessionID, OnMessage: onMsg})
}

func (m *ControllerManager) Dispatch(ctx context.Context, action domain.Action) error {
	m.engine.Send(m.pid, wsDispatchMsg{ctx: ctx, action: action})
	return nil
}

func (m *ControllerManager) Preload(ctx context.Context, action domain.Action) error {
	m.engine.Send(m.pid, wsPreloadMsg{ctx: ctx, action: action})
	return nil
}

func (m *ControllerManager) AbortCurrent(ctx context.Context, reason string) error {
	m.engine.Send(m.pid, wsAbortMsg{ctx: ctx, reason: reason})
	return nil
}

func (m *ControllerManager) RecordResult(res domain.ExecutionResult) {
	m.engine.Send(m.pid, RecordResultMsg{Result: res})
}

func (m *ControllerManager) GetSuccessRate() float64 {
	res, err := m.engine.Request(m.pid, GetSuccessRateMsg{}, time.Second).Result()
	if err != nil {
		return 1.0
	}
	return res.(float64)
}

func (m *ControllerManager) GetRecentFailures() []domain.ExecutionResult {
	res, err := m.engine.Request(m.pid, GetRecentFailuresMsg{}, time.Second).Result()
	if err != nil {
		return nil
	}
	return res.([]domain.ExecutionResult)
}

func (m *ControllerManager) IsReady() bool {
	res, err := m.engine.Request(m.pid, "is_ready", time.Second).Result()
	if err != nil {
		return false
	}
	return res.(bool)
}

func (m *ControllerManager) Close() error {
	m.engine.Poison(m.pid)
	return nil
}

// Internal Actor State
type managerActor struct {
	logger      *slog.Logger
	activeWS    *actor.PID
	lastResults []domain.ExecutionResult
}

func (a *managerActor) Receive(ctx *actor.Context) {
	switch msg := ctx.Message().(type) {
	
	case RegisterWSConnMsg:
		if a.activeWS != nil {
			a.logger.Info("Poisoning old WS connection to make room for new one")
			ctx.Engine().Poison(a.activeWS)
		}
		
		onClose := func() {
			ctx.Engine().Send(ctx.PID(), wsClosedEventMsg{PID: a.activeWS})
		}
		
		pid := ctx.SpawnChild(NewWSActor(msg.Conn, a.logger, msg.SessionID, msg.OnMessage, onClose), "ws_bot_conn")
		a.activeWS = pid

	case wsClosedEventMsg:
		if a.activeWS != nil && a.activeWS.Equals(msg.PID) {
			a.activeWS = nil
			a.logger.Warn("Bot connection closed and cleared")
		}

	case wsDispatchMsg, wsPreloadMsg, wsAbortMsg:
		if a.activeWS != nil {
			ctx.Forward(a.activeWS)
		} else {
			a.logger.Warn("Dropped control message: no active bot connection")
		}

	case string:
		if msg == "is_ready" {
			ctx.Respond(a.activeWS != nil)
		}

	case RecordResultMsg:
		a.lastResults = append(a.lastResults, msg.Result)
		if len(a.lastResults) > 20 {
			a.lastResults = a.lastResults[1:]
		}

	case GetSuccessRateMsg:
		if len(a.lastResults) == 0 {
			ctx.Respond(1.0)
			return
		}
		successes := 0
		for _, res := range a.lastResults {
			if res.Success && res.Progress >= 1.0 {
				successes++
			}
		}
		ctx.Respond(float64(successes) / float64(len(a.lastResults)))

	case GetRecentFailuresMsg:
		var failures []domain.ExecutionResult
		for _, res := range a.lastResults {
			if !res.Success || res.Progress < 1.0 {
				failures = append(failures, res)
			}
		}
		ctx.Respond(failures)
	}
}
