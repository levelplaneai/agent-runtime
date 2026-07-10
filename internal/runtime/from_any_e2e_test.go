package runtime

import (
	"context"
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// TestFromAnyRouter_E2E runs the from_any_router testdata bundle end-to-end with
// a deterministic router (no LLM needed) and asserts that the coalescing
// `should_cost` output holds only the executed terminal's value — the
// unexecuted branch contributes a null that from_any skips past.
func TestFromAnyRouter_E2E(t *testing.T) {
	b, err := bundle.Load("../../testdata/from_any_router.agent")
	if err != nil {
		t.Fatalf("load bundle: %v", err)
	}
	if errs := bundle.Validate(b); len(errs) > 0 {
		for _, e := range errs {
			t.Logf("validation error: %v", e)
		}
		t.Fatal("bundle validation failed")
	}

	newReg := func() *Registry {
		reg := NewRegistry()
		reg.Register("emit.a@v1", bundle.ToolSignature{}, ToolFunc(
			func(_ context.Context, _ map[string]any) (map[string]any, error) {
				return map[string]any{"value": "A-result"}, nil
			}))
		reg.Register("emit.b@v1", bundle.ToolSignature{}, ToolFunc(
			func(_ context.Context, _ map[string]any) (map[string]any, error) {
				return map[string]any{"value": "B-result"}, nil
			}))
		return reg
	}

	cases := []struct {
		route string
		want  string
	}{
		{route: "a", want: "A-result"}, // emit_a wins (first candidate)
		{route: "z", want: "B-result"}, // default → emit_b wins (emit_a never ran → null skipped)
	}
	for _, tc := range cases {
		t.Run(tc.route, func(t *testing.T) {
			out, err := RunFlow(context.Background(), b, map[string]any{"route": tc.route}, newReg(), nil, nil)
			if err != nil {
				t.Fatalf("RunFlow error: %v", err)
			}
			if out["should_cost"] != tc.want {
				t.Errorf("route %q: expected should_cost=%q, got %v", tc.route, tc.want, out["should_cost"])
			}
			if out["route_echo"] != tc.route {
				t.Errorf("route %q: expected route_echo=%q, got %v", tc.route, tc.route, out["route_echo"])
			}
		})
	}
}
