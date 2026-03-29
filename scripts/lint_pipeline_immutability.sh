#!/bin/bash
# scripts/lint_policy_authority.sh

echo "Checking for rogue policy overrides outside policy package..."
ROGUE=$(grep -r "POLICY VIOLATION" internal/ | grep -v "internal/policy/")

if [ -n "$ROGUE" ]; then
    echo "ERROR: Hardcoded policy constraints found outside internal/policy/!"
    echo "$ROGUE"
    exit 1
fi
echo "Policy authority checks passed."