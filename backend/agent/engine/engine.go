package engine

import (
	"context"
	"errors"
	"fmt"
	"io"

	"qingfly/edu-mall/agent/capability"
)

// RunRequest starts an agent run.
type RunRequest struct {
	RunID     string   // idempotency key for the run
	AgentID   string   // which agent config (determines bound capabilities)
	Prompt    string
	Caller    capability.Scoper // the principal invoking (scopes checked per tool)
}

// Result is the terminal outcome of a run.
type Result struct {
	RunID     string   `json:"run_id"`
	Final     string   `json:"final"`
	Iterations int     `json:"iterations"`
	Forced    bool     `json:"forced"` // true if max-iterations guardrail forced the answer
}

// Engine wires the runtime pieces together.
type Engine struct {
	model    Model
	reg      *capability.Registry
	policy   *capability.Policy
	exec     Executor
	checks   CheckpointStore
	maxIter  int
}

// Options configure an Engine.
type Options struct {
	Model      Model
	Registry   *capability.Registry
	Policy     *capability.Policy
	Executor   Executor
	Checkpoints CheckpointStore
	MaxIterations int
}

// New builds an Engine, applying sane defaults.
func New(o Options) (*Engine, error) {
	if o.Model == nil || o.Registry == nil || o.Policy == nil || o.Executor == nil || o.Checkpoints == nil {
		return nil, errors.New("engine: model/registry/policy/executor/checkpoints are required")
	}
	if o.MaxIterations <= 0 {
		o.MaxIterations = 8
	}
	return &Engine{model: o.Model, reg: o.Registry, policy: o.Policy, exec: o.Executor, checks: o.Checkpoints, maxIter: o.MaxIterations}, nil
}

// emit writes one SSE event to w; a nil w is a no-op (non-streaming callers).
func emit(w io.Writer, e Event) {
	if w == nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %q\n\n", e.Kind, e.Payload)
}

// Run executes the ReAct loop, streaming events to w (may be nil), and returns the result.
// It checkpoints after every iteration so Resume can continue after interrupt/crash.
func (e *Engine) Run(ctx context.Context, req RunRequest, w io.Writer) (*Result, error) {
	if req.RunID == "" {
		return nil, errors.New("engine: run_id required")
	}
	history := []string{}
	startIter := 0
	// resume from checkpoint if present (idempotent re-entry)
	if cp, err := e.checks.Load(ctx, req.RunID); err == nil {
		history = cp.History
		startIter = cp.Iteration
	} else if !errors.Is(err, ErrNoCheckpoint) {
		return nil, err
	}

	for it := startIter; ; it++ {
		step, err := e.model.Next(ctx, req.RunID, req.Prompt, history)
		if err != nil {
			return nil, fmt.Errorf("engine: model next at iter %d: %w", it, err)
		}
		if step.Thought != "" {
			history = append(history, "thought: "+step.Thought)
			emit(w, Event{Kind: EvtThought, Iteration: it, Payload: step.Thought})
		}
		// terminal answer?
		if step.Final != nil {
			emit(w, Event{Kind: EvtFinal, Iteration: it, Payload: step.Final.Text})
			_ = e.checks.Delete(ctx, req.RunID)
			return &Result{RunID: req.RunID, Final: step.Final.Text, Iterations: it + 1}, nil
		}
		// guardrail: force final before unbounded loop
		if it >= e.maxIter-1 && step.Action != nil {
			forced := fmt.Sprintf("[guardrail] max iterations (%d) reached; stopping with last observation.", e.maxIter)
			history = append(history, forced)
			emit(w, Event{Kind: EvtFinal, Iteration: it, Payload: forced})
			_ = e.checks.Delete(ctx, req.RunID)
			return &Result{RunID: req.RunID, Final: forced, Iterations: it + 1, Forced: true}, nil
		}
		if step.Action == nil {
			return nil, errors.New("engine: model returned neither action nor final")
		}
		// resolve capability + enforce policy (default-deny)
		cap, err := e.reg.Get(step.Action.CapabilityName, step.Action.Version)
		if err != nil {
			return nil, fmt.Errorf("engine: resolve capability: %w", err)
		}
		if err := e.policy.Check(ctx, req.AgentID, req.RunID, req.Caller, cap, step.Action.Args); err != nil {
			// policy block => surface as observation so model can adapt, or interrupt for approval
			history = append(history, "policy-denied: "+err.Error())
			emit(w, Event{Kind: EvtInterrupt, Iteration: it, Payload: "policy: " + err.Error()})
			cp := &Checkpoint{RunID: req.RunID, Prompt: req.Prompt, History: history, Iteration: it + 1}
			if serr := e.checks.Save(ctx, cp); serr != nil {
				return nil, serr
			}
			return &Result{RunID: req.RunID, Final: "interrupted: " + err.Error(), Iterations: it + 1}, nil
		}
		emit(w, Event{Kind: EvtAction, Iteration: it, Payload: cap.Name + "(" + step.Action.Args + ")"})
		obs, err := e.exec.Execute(ctx, cap, step.Action.Args)
		if err != nil {
			obs = "error: " + err.Error()
		}
		history = append(history, "observation: "+obs)
		emit(w, Event{Kind: EvtObservation, Iteration: it, Payload: obs})
		// checkpoint for resumability
		cp := &Checkpoint{RunID: req.RunID, Prompt: req.Prompt, History: history, Iteration: it + 1}
		if err := e.checks.Save(ctx, cp); err != nil {
			return nil, err
		}
	}
}
