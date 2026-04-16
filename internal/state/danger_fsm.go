package state

import (
	"sync"
	"time"

	"david22573/synaptic-mc/internal/domain"
)

type DangerState string

const (
	DangerSafe       DangerState = "SAFE"
	DangerAlert      DangerState = "ALERT"
	DangerEscape     DangerState = "ESCAPE"
	DangerRecovering DangerState = "RECOVERING"
)

type DangerAnalyzer struct {
	mu sync.RWMutex

	currentState    DangerState
	lastStateChange time.Time
	lastEscapeTime  time.Time
}

func NewDangerAnalyzer() *DangerAnalyzer {
	return &DangerAnalyzer{
		currentState:    DangerSafe,
		lastStateChange: time.Now(),
	}
}

func (a *DangerAnalyzer) Update(state domain.GameState) DangerState {
	a.mu.Lock()
	defer a.mu.Unlock()

	danger := a.calculateDanger(state)
	now := time.Now()

	switch a.currentState {
	case DangerSafe:
		if danger >= 0.8 {
			a.transition(DangerEscape, now)
		} else if danger >= 0.6 {
			a.transition(DangerAlert, now)
		}
	case DangerAlert:
		if danger >= 0.8 {
			a.transition(DangerEscape, now)
		} else if danger <= 0.4 {
			a.transition(DangerSafe, now)
		}
	case DangerEscape:
		// Persistent escape until danger is consistently low (Hysteresis)
		if danger <= 0.4 && time.Since(a.lastStateChange) > 3*time.Second {
			a.transition(DangerRecovering, now)
		}
	case DangerRecovering:
		// Cooldown window: ignore retrigger unless lethal within 3s
		if danger >= 0.9 || (danger >= 0.6 && time.Since(a.lastEscapeTime) > 3*time.Second) {
			a.transition(DangerEscape, now)
		} else if danger <= 0.2 && time.Since(a.lastStateChange) > 5*time.Second {
			a.transition(DangerSafe, now)
		}
	}

	return a.currentState
}

func (a *DangerAnalyzer) GetState() DangerState {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.currentState
}

func (a *DangerAnalyzer) transition(newState DangerState, now time.Time) {
	if a.currentState != newState {
		if a.currentState == DangerEscape {
			a.lastEscapeTime = now
		}
		a.currentState = newState
		a.lastStateChange = now
	}
}

func (a *DangerAnalyzer) calculateDanger(state domain.GameState) float64 {
	var maxDanger float64

	// 1. Threat Distance
	for _, t := range state.Threats {
		d := 1.0 - (t.Distance / 16.0)
		if d > maxDanger {
			maxDanger = d
		}
	}

	// 2. Health
	hpDanger := 1.0 - (state.Health / 20.0)
	if hpDanger > maxDanger {
		maxDanger = hpDanger
	}

	// 3. Hazards
	for _, f := range state.Feedback {
		if f.Type == "hazard" {
			maxDanger = 1.0
			break
		}
	}

	return maxDanger
}
