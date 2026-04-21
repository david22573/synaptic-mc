package state

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"david22573/synaptic-mc/internal/domain"
	"david22573/synaptic-mc/internal/memory"
)

type Service struct {
	bus          domain.EventBus
	memStore     memory.Store
	logger       *slog.Logger
	stateVersion atomic.Uint64
	currentState atomic.Pointer[domain.VersionedState]

	heatmap   *ThreatHeatmap
	predictor *EnemyPredictor
}

func NewService(bus domain.EventBus, memStore memory.Store, logger *slog.Logger) *Service {
	s := &Service{
		bus:       bus,
		memStore:  memStore,
		logger:    logger.With(slog.String("component", "state_service")),
		heatmap:   NewThreatHeatmap(),
		predictor: NewEnemyPredictor(),
	}

	// Initialize with an empty state so loads never panic
	s.currentState.Store(&domain.VersionedState{})

	if bus != nil {
		s.subscribe(bus)
	}

	// Background Heatmap Decay
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		for range ticker.C {
			s.heatmap.Decay()
		}
	}()

	return s
}

func (s *Service) SetEventBus(bus domain.EventBus) {
	s.bus = bus
	s.subscribe(bus)
}

func (s *Service) subscribe(bus domain.EventBus) {
	// Wire up the bus listeners
	bus.Subscribe(domain.EventTypeStateTick, domain.FuncHandler(s.handleStateTick))

	// Phase 5: Listen for task completion to alter the world model
	bus.Subscribe(domain.EventTypeTaskEnd, domain.FuncHandler(s.handleTaskEnd))
}
func (s *Service) GetCurrentState() domain.VersionedState {
	if ptr := s.currentState.Load(); ptr != nil {
		return *ptr
	}
	return domain.VersionedState{}
}

func (s *Service) handleStateTick(ctx context.Context, event domain.DomainEvent) {
	curr := s.GetCurrentState()
	newState := Reduce(curr.State, event)

	// Near-Cheating: Record threats in heatmap
	for _, t := range newState.Threats {
		danger := 0.1
		if t.Distance < 5 {
			danger = 0.5
		}
		s.heatmap.RecordThreat(newState.Position, danger)
	}

	// Near-Cheating: Enemy Prediction (Mock)
	_ = s.predictor.Predict(newState.Threats)

	// World Memory Graph: Persist significant features
	if s.memStore != nil {
		for _, poi := range newState.POIs {
			nodeType := ""
			switch {
			case strings.Contains(strings.ToLower(poi.Name), "village"):
				nodeType = "village"
			case strings.Contains(strings.ToLower(poi.Name), "cave"):
				nodeType = "cave"
			case strings.Contains(strings.ToLower(poi.Name), "chest") || strings.Contains(strings.ToLower(poi.Name), "bed"):
				nodeType = "safe_base"
			}

			if nodeType != "" {
				score := 10.0
				if nodeType == "safe_base" {
					score = 50.0
				} else if nodeType == "village" {
					score = 100.0
				}
				_ = s.memStore.MarkWorldNode(ctx, domain.WorldNode{
					Name:  poi.Name,
					Kind:  nodeType,
					Pos:   poi.Position,
					Score: score,
				})
			}
		}
	}

	// Instant Reactions: High-priority survival check
	isLava := false
	isFalling := false
	for _, fb := range newState.Feedback {
		cause := strings.ToLower(fb.Cause)
		if strings.Contains(cause, "lava") {
			isLava = true
		}
		if strings.Contains(cause, "fall") {
			isFalling = true
		}
	}
	lowHealth := newState.Health < 8.0
	surroundedCount := 0
	for _, t := range newState.Threats {
		if t.Distance < 4.0 {
			surroundedCount++
		}
	}
	surrounded := surroundedCount >= 3

	if isLava || lowHealth || isFalling || surrounded {
		s.bus.Publish(ctx, domain.DomainEvent{
			SessionID: event.SessionID,
			Type:      domain.EventTypePanic,
			Payload:   []byte(fmt.Sprintf(`{"cause": "survival_critical", "is_lava": %v, "low_health": %v, "is_falling": %v, "is_surrounded": %v}`, isLava, lowHealth, isFalling, surrounded)),
			CreatedAt: time.Now(),
		})
	}

	s.saveState(ctx, newState, event)
}

// Phase 5: Mutate state actively based on execution feedback
func (s *Service) handleTaskEnd(ctx context.Context, event domain.DomainEvent) {
	curr := s.GetCurrentState()
	nextState := Reduce(curr.State, event)

	// If reducer added a danger zone or changed visited chunks, persist it
	if len(nextState.DangerZones) != len(curr.State.DangerZones) || 
	   len(nextState.VisitedChunks) != len(curr.State.VisitedChunks) {
		s.saveState(ctx, nextState, event)
	}
}

func (s *Service) saveState(ctx context.Context, newState domain.GameState, triggerEvent domain.DomainEvent) {
	vState := domain.VersionedState{
		Version: s.stateVersion.Add(1),
		State:   newState,
	}

	s.currentState.Store(&vState)

	payload, _ := json.Marshal(vState)

	// Emit the normalized internal event so the rest of the system can react
	s.bus.Publish(ctx, domain.DomainEvent{
		SessionID: triggerEvent.SessionID,
		Trace:     triggerEvent.Trace,
		Type:      domain.EventTypeStateUpdated,
		Payload:   payload,
		CreatedAt: time.Now(),
	})
}
