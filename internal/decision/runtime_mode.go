package decision

import (
	"sync"
	"time"
)

type RuntimeMode string

const (
	ModeNormal   RuntimeMode = "normal"
	ModeUnstable RuntimeMode = "unstable"
	ModeCrisis   RuntimeMode = "crisis"
	ModeRecovery RuntimeMode = "recovery"
)

type ModeManager struct {
	mu sync.RWMutex

	current            RuntimeMode
	lastStabilityCheck time.Time
	failureWindow      []time.Time
}

func NewModeManager() *ModeManager {
	return &ModeManager{
		current: ModeNormal,
		lastStabilityCheck: time.Now(),
		failureWindow:      make([]time.Time, 0),
	}
}

func (m *ModeManager) RecordFailure() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.failureWindow = append(m.failureWindow, time.Now())
	m.evaluate()
}

func (m *ModeManager) RecordSuccess() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.current == ModeCrisis || m.current == ModeUnstable {
		m.current = ModeRecovery
	} else if m.current == ModeRecovery {
		m.current = ModeNormal
	}
}

func (m *ModeManager) GetMode() RuntimeMode {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.current
}

func (m *ModeManager) evaluate() {
	// Clean up old failures (older than 1 minute)
	cutoff := time.Now().Add(-1 * time.Minute)
	valid := make([]time.Time, 0)
	for _, t := range m.failureWindow {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	m.failureWindow = valid

	count := len(m.failureWindow)
	if count >= 10 {
		m.current = ModeCrisis
	} else if count >= 5 {
		m.current = ModeUnstable
	} else if m.current == ModeCrisis || m.current == ModeUnstable {
		m.current = ModeRecovery
	}
}
