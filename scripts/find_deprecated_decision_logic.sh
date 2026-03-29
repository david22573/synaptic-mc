#!/bin/bash
echo "Identifying old decision logic for removal..."
grep -rn "func (.*Validator) Validate" internal/decision/validator.go
grep -rn "func (.*Simulator) RankAndSelect" internal/decision/simulator.go
echo "These functions should be deleted after Step 1.3 is complete."