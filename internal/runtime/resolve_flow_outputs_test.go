package runtime

import (
	"strings"
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// flowWith wraps output bindings in a minimal flow for resolveFlowOutputs.
func flowWith(outputs map[string]bundle.FlowOutputBinding) bundle.Flow {
	return bundle.Flow{Outputs: outputs}
}

func TestResolveFlowOutputs_FromAny_FirstNonNullWins(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{})
	// "winner" executed with a value; "loser" also executed but later in the list.
	ctx.SetNodeOutput("winner", map[string]any{"v": "first"})
	ctx.SetNodeOutput("loser", map[string]any{"v": "second"})

	flow := flowWith(map[string]bundle.FlowOutputBinding{
		"out": {FromAny: []string{"$.winner.output.v", "$.loser.output.v"}},
	})
	res, err := resolveFlowOutputs(flow, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res["out"] != "first" {
		t.Errorf("expected 'first', got %v", res["out"])
	}
}

func TestResolveFlowOutputs_FromAny_SkipsUnexecutedNull(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{})
	// "chosen" executed; "skipped_branch" never executed → resolves to nil.
	ctx.SetNodeOutput("chosen", map[string]any{"v": "value"})

	flow := flowWith(map[string]bundle.FlowOutputBinding{
		"out": {FromAny: []string{"$.skipped_branch.output.v", "$.chosen.output.v"}},
	})
	res, err := resolveFlowOutputs(flow, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res["out"] != "value" {
		t.Errorf("expected 'value' (skipping unexecuted null), got %v", res["out"])
	}
}

func TestResolveFlowOutputs_FromAny_AllNullYieldsNil(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{})
	flow := flowWith(map[string]bundle.FlowOutputBinding{
		"out": {FromAny: []string{"$.a.output.v", "$.b.output.v"}},
	})
	res, err := resolveFlowOutputs(flow, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := res["out"]; !ok {
		t.Fatalf("expected 'out' key present")
	}
	if res["out"] != nil {
		t.Errorf("expected nil when all candidates null, got %v", res["out"])
	}
}

func TestResolveFlowOutputs_FromAny_PropagatesTraversalError(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{})
	// "n" executed with a string at .a; traversing .a.b is a genuine error, not
	// a null to skip past.
	ctx.SetNodeOutput("n", map[string]any{"a": "string-value"})

	flow := flowWith(map[string]bundle.FlowOutputBinding{
		"out": {FromAny: []string{"$.n.output.a.b", "$.n.output.a"}},
	})
	_, err := resolveFlowOutputs(flow, ctx)
	if err == nil {
		t.Fatalf("expected traversal error, got nil")
	}
	if !strings.Contains(err.Error(), "flow output \"out\"") {
		t.Errorf("expected error tagged with output name, got %v", err)
	}
}

func TestResolveFlowOutputs_FromAny_InputsCandidate(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{"x": "from-input"})
	flow := flowWith(map[string]bundle.FlowOutputBinding{
		"out": {FromAny: []string{"$.unrun.output.v", "$.inputs.x"}},
	})
	res, err := resolveFlowOutputs(flow, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res["out"] != "from-input" {
		t.Errorf("expected 'from-input', got %v", res["out"])
	}
}

func TestResolveFlowOutputs_From_Unchanged(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{})
	ctx.SetNodeOutput("n", map[string]any{"v": "plain"})
	flow := flowWith(map[string]bundle.FlowOutputBinding{
		"out": {From: "$.n.output.v"},
	})
	res, err := resolveFlowOutputs(flow, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res["out"] != "plain" {
		t.Errorf("expected 'plain', got %v", res["out"])
	}
}
