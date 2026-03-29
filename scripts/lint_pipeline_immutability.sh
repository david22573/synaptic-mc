#!/bin/bash
# scripts/lint_pipeline_immutability.sh
# Ensures pipeline stages don't mutate previously set artifacts.

echo "Checking for direct mutations of PipelineState artifacts..."

# Catch lines like `state.Plan.Objective = ...` or `state.Normalized.Tasks[0] = ...`
# This is a basic text heuristic. For deeper enforcement, an AST linter would be needed.
VIOLATIONS=$(grep -r --include="*.go" -E "state\.(Plan|Normalized|Validation|Simulation|Policy)\..*=" internal/pipeline/)

if [ -n "$VIOLATIONS" ]; then
    echo "ERROR: Direct mutation of PipelineState artifacts detected! Append new artifacts instead of modifying existing ones."
    echo "$VIOLATIONS"
    exit 1
fi

echo "Pipeline immutability checks passed."