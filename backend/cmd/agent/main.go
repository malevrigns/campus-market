// Command agent runs the edu-mall AI Agent service (Part 4 of the course).
// It wires a registry of atomic capabilities + policy + executor + model -> engine ->
// SSE handler and serves on :8084. In production the Model is an eino adapter, the
// Executor dispatches to local tools / MCP / gRPC, checkpoints go to Redis; here simple
// in-memory implementations keep it runnable offline.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strings"

	"qingfly/edu-mall/agent/capability"
	"qingfly/edu-mall/agent/engine"
	"qingfly/edu-mall/agent/handler"
)

// demoModel is a deterministic planner: first call issues one tool call, then answers.
// In production this is replaced by an eino ChatModel adapter.
type demoModel struct{}

func (demoModel) Next(_ context.Context, runID, prompt string, history []string) (*engine.Step, error) {
	for _, h := range history {
		if strings.HasPrefix(h, "observation:") {
			return &engine.Step{Thought: "tool returned data", Final: &engine.FinalAnswer{Text: "done: " + prompt}}, nil
		}
	}
	return &engine.Step{
		Thought: "need to look up order",
		Action:  &engine.ToolCall{CapabilityName: "order.query", Version: "v1", Args: "{}"},
	}, nil
}

// demoExecutor runs registered capabilities with a canned observation.
type demoExecutor struct{ reg *capability.Registry }

func (e *demoExecutor) Execute(_ context.Context, c capability.Capability, args string) (string, error) {
	return c.Name + ":ok args=" + args, nil
}

// autoApprover approves all calls (dev only); production uses human-in-the-loop for
// RiskIrreversible / RiskExternalSideEffect.
type autoApprover struct{}

func (autoApprover) Approve(context.Context, string, capability.Capability, string) bool { return true }

func main() {
	reg := capability.NewRegistry()
	reg.Register(capability.Capability{Name: "order.query", Version: "v1", Description: "query order status", Risk: capability.RiskRead, Requires: []string{"order.read"}})
	reg.Register(capability.Capability{Name: "order.refund", Version: "v1", Description: "refund an order", Risk: capability.RiskIrreversible, Requires: []string{"order.refund"}})

	policy := capability.NewPolicy(autoApprover{})
	eng, err := engine.New(engine.Options{
		Model:         demoModel{},
		Registry:      reg,
		Policy:        policy,
		Executor:      &demoExecutor{reg: reg},
		Checkpoints:   engine.NewMemoryCheckpointStore(),
		MaxIterations: 8,
	})
	if err != nil {
		log.Fatalf("[agent] engine init: %v", err)
	}

	mux := http.NewServeMux()
	h := handler.New(eng)
	for route, fn := range h.Routes() {
		mux.HandleFunc(route, fn)
	}
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	addr := ":8084"
	if v := os.Getenv("AGENT_ADDR"); v != "" {
		addr = v
	}
	cors := func(h http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Accept")
			if r.Method == "OPTIONS" { w.WriteHeader(http.StatusNoContent); return }
			h.ServeHTTP(w, r)
		})
	}
	if err := http.ListenAndServe(addr, cors(mux)); err != nil {
		log.Fatalf("[agent] serve: %v", err)
	}
}
