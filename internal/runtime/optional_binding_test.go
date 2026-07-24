package runtime

import (
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// An optional binding degrades an unresolvable nested path to null instead of
// failing the node — covering both a null parent (prior_run is nil on a first
// run) and a missing key (a node absent from the prior run / another lane).
func TestResolveNodeInputs_OptionalDegradesToNull(t *testing.T) {
	cases := []struct {
		name  string
		prior any
	}{
		{"null parent", nil},
		{"missing key", map[string]any{"other_node": map[string]any{}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := NewExecutionContext(map[string]any{"prior_run": tc.prior})
			node := bundle.Node{Inputs: map[string]bundle.InputBinding{
				"prior": {From: "$.inputs.prior_run.cast_analyze", Optional: true},
			}}
			resolved, err := resolveNodeInputs(node, ctx)
			if err != nil {
				t.Fatalf("optional binding must not error, got %v", err)
			}
			if v, ok := resolved["prior"]; !ok || v != nil {
				t.Errorf("want nil for %q, got %v (present=%v)", tc.name, v, ok)
			}
		})
	}
}

// Without optional, the same unresolvable path fails the node — proving optional
// is what saves it (not some other leniency), and that strict paths still error.
func TestResolveNodeInputs_NonOptionalStillErrors(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{"prior_run": nil})
	node := bundle.Node{Inputs: map[string]bundle.InputBinding{
		"prior": {From: "$.inputs.prior_run.cast_analyze"},
	}}
	if _, err := resolveNodeInputs(node, ctx); err == nil {
		t.Error("non-optional binding into a null parent should error")
	}
}

// When the path resolves, optional returns the real value unchanged.
func TestResolveNodeInputs_OptionalPresentReturnsValue(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{
		"prior_run": map[string]any{
			"cast_analyze": map[string]any{"output": map[string]any{"plan": "x"}},
		},
	})
	node := bundle.Node{Inputs: map[string]bundle.InputBinding{
		"prior": {From: "$.inputs.prior_run.cast_analyze", Optional: true},
	}}
	resolved, err := resolveNodeInputs(node, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := resolved["prior"].(map[string]any)
	if !ok || got["output"] == nil {
		t.Errorf("optional-present should return the real value, got %v", resolved["prior"])
	}
}
