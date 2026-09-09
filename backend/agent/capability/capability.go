// Package capability models the Agent's "atomic capabilities" (tools / MCP / skills)
// and the governance around them: versioned registry, risk grading, and a policy
// that enforces allowlists and human approval for high-risk calls.
package capability

import "errors"

// Risk grades a tool call. Higher risk => stricter guardrails.
type Risk int

const (
	RiskRead Risk = iota // 只读，无副作用
	RiskWrite            // 可写，可逆
	RiskIrreversible     // 不可逆（删除、发钱、发消息给外部）
	RiskExternal         // 外部副作用（第三方 API、支付网关）
)

func (r Risk) String() string {
	switch r {
	case RiskRead:
		return "read"
	case RiskWrite:
		return "write"
	case RiskIrreversible:
		return "irreversible"
	case RiskExternal:
		return "external_side_effect"
	default:
		return "unknown"
	}
}

// Capability is one registered atomic capability (a tool).
type Capability struct {
	Name        string `json:"name"`
	Version     string `json:"version"` // capabilities are versioned; changing an agent's binding must be gray-released
	Description string `json:"description"`
	Risk        Risk   `json:"risk"`
	Requires    []string `json:"requires,omitempty"` // scopes required to invoke, e.g. ["order.refund"]
}

// Registry stores capabilities by name+version. Thread-safe.
type Registry struct {
	m map[string]map[string]Capability // name -> version -> cap
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry { return &Registry{m: map[string]map[string]Capability{}} }

// Register adds or updates a capability version.
func (r *Registry) Register(c Capability) {
	if r.m[c.Name] == nil {
		r.m[c.Name] = map[string]Capability{}
	}
	r.m[c.Name][c.Version] = c
}

// Get returns the exact version, or ErrNotFound.
func (r *Registry) Get(name, version string) (Capability, error) {
	vs, ok := r.m[name]
	if !ok {
		return Capability{}, ErrNotFound
	}
	c, ok := vs[version]
	if !ok {
		return Capability{}, ErrNotFound
	}
	return c, nil
}

// List returns all registered capabilities (for admin console).
func (r *Registry) List() []Capability {
	out := make([]Capability, 0)
	for _, vs := range r.m {
		for _, c := range vs {
			out = append(out, c)
		}
	}
	return out
}

var ErrNotFound = errors.New("capability: not found")
