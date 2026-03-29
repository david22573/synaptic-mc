// internal/policy/composite.go
package policy

import "context"

// CompositePolicy allows chaining multiple rule engines (e.g., Survival -> Progression -> Ethical).
type CompositePolicy struct {
	policies []Engine
}

func NewCompositePolicy(policies ...Engine) *CompositePolicy {
	return &CompositePolicy{policies: policies}
}

func (c *CompositePolicy) Decide(ctx context.Context, input DecisionInput) Decision {
	for _, p := range c.policies {
		decision := p.Decide(ctx, input)
		if !decision.IsApproved {
			return decision // Fast fail on the first policy rejection
		}
	}
	return Decision{IsApproved: true}
}
