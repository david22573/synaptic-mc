package planner

import (
	"io"
	"log/slog"
	"testing"

	"david22573/synaptic-mc/internal/domain"
)

func testFeedbackAnalyzer() *FeedbackAnalyzer {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewFeedbackAnalyzer(domain.NewWorldModel(), logger)
}

func TestFeedbackAnalyzerTimeoutBackoffUsesReposition(t *testing.T) {
	analyzer := testFeedbackAnalyzer()

	intent := domain.ActionIntent{
		ID:     "task-1",
		Action: "explore",
		Target: "surroundings",
	}
	res := domain.ExecutionResult{
		Success:  false,
		Cause:    domain.CauseTimeout,
		Progress: 0,
	}

	next := analyzer.Analyze(intent, res)
	if next == nil {
		t.Fatal("expected adapted intent")
	}
	if next.Action != "reposition" {
		t.Fatalf("expected reposition fallback, got %s", next.Action)
	}
}

func TestFeedbackAnalyzerBlockedWithoutTargetLocationUsesReposition(t *testing.T) {
	analyzer := testFeedbackAnalyzer()

	intent := domain.ActionIntent{
		ID:     "task-2",
		Action: "mine",
		Target: "iron_ore",
	}
	res := domain.ExecutionResult{
		Success:  false,
		Cause:    domain.CauseBlocked,
		Progress: 0,
	}

	next := analyzer.Analyze(intent, res)
	if next == nil {
		t.Fatal("expected adapted intent")
	}
	if next.Action != "reposition" {
		t.Fatalf("expected reposition fallback, got %s", next.Action)
	}
}
