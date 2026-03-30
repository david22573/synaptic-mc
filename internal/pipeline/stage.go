package pipeline

// StageSnapshot captures the exact state before and after a stage for deterministic debugging.
type StageSnapshot struct {
	StageName string
	Input     PipelineState
	Output    PipelineState
}
