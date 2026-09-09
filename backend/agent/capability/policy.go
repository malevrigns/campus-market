package capability

import (
	"context"
	"errors"
)

var (
	ErrNotBound    = errors.New("capability: not bound to this agent")
	ErrMissingScope = errors.New("capability: caller missing required scope")
	ErrNeedsApproval = errors.New("capability: high-risk call requires human approval")
)

// Scoper is what the policy checks scopes against (the auth.Principal in prod).
type Scoper interface {
	Scopes() []string
}

// Policy enforces per-agent capability binding + scope + risk-based approval.
type Policy struct {
	// bindings: agentID -> set of "name@version" allowed.
	bindings map[string]map[string]bool
	// approver decides whether a pending high-risk call is approved (human-in-the-loop).
	approver Approver
}

// Approver is human-in-the-loop approval for high-risk calls. Interface so the engine
// can be tested with an auto-approver and wired to a real approval service in prod.
type Approver interface {
	Approve(ctx context.Context, runID string, cap Capability, args string) bool
}

// NewPolicy builds a policy with the given approver (nil => high-risk always blocked).
func NewPolicy(approver Approver) *Policy {
	return &Policy{bindings: map[string]map[string]bool{}, approver: approver}
}

// Bind allows agentID to use cap at its version (admin console writes these; changes gray-release).
func (p *Policy) Bind(agentID string, c Capability) {
	if p.bindings[agentID] == nil {
		p.bindings[agentID] = map[string]bool{}
	}
	p.bindings[agentID][c.Name+"@"+c.Version] = true
}

// Check validates a proposed tool call before execution (default-deny).
func (p *Policy) Check(ctx context.Context, agentID, runID string, caller Scoper, c Capability, args string) error {
	if !p.bindings[agentID][c.Name+"@"+c.Version] {
		return ErrNotBound
	}
	scopes := caller.Scopes()
	for _, req := range c.Requires {
		if !has(scopes, req) {
			return ErrMissingScope
		}
	}
	if c.Risk >= RiskIrreversible && p.approver != nil && !p.approver.Approve(ctx, runID, c, args) {
		return ErrNeedsApproval
	}
	return nil
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
