// Package engine is the Agent runtime: a ReAct-style think→act loop over atomic
// capabilities, with guardrails (max iterations / forced final answer), checkpoints
// for interrupt-resume (human-in-the-loop), and SSE-style streaming events. The LLM
// itself is behind the Model interface so eino plugs in in production while this core
// stays stdlib-only, buildable and testable offline.
package engine

import (
	"context"
	"errors"
)

var (
	ErrMaxIterations = errors.New("agent: max iterations reached (forced final answer)")
	ErrNoCheckpoint  = errors.New("agent: no checkpoint to resume")
)

// EventKind classifies a streaming event emitted during a run (SSE).
type EventKind string

const (
	EvtThought   EventKind = "thought"   // model reasoning
	EvtAction    EventKind = "action"    // a tool call about to execute
	EvtObservation EventKind = "observation" // tool result fed back
	EvtFinal     EventKind = "final"     // terminal answer
	EvtInterrupt EventKind = "interrupt" // paused for human approval/resume
)

// Event is one SSE frame.
type Event struct {
	Kind      EventKind `json:"kind"`
	Iteration int       `json:"iteration"`
	Payload   string    `json:"payload"`
}

// ToolCall is a proposed action from the model.
type ToolCall struct {
	CapabilityName string `json:"capability"`
	Version        string `json:"version"`
	Args           string `json:"args"`
}

// FinalAnswer signals the model wants to stop and return an answer.
type FinalAnswer struct {
	Text string `json:"text"`
}

// Step is what the Model produces each iteration: either act or finish.
type Step struct {
	Thought     string        `json:"thought"`
	Action      *ToolCall     `json:"action,omitempty"`
	Final       *FinalAnswer  `json:"final,omitempty"`
}

// Model abstracts the LLM planner (eino adapter in prod). Given context + history it
// returns the next step. Keep it pure so runs are deterministic under tests.
type Model interface {
	Next(ctx context.Context, runID, prompt string, history []string) (*Step, error)
}
