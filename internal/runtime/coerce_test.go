package runtime

import (
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

func decl(pairs map[string]string) map[string]bundle.FlowInputField {
	d := map[string]bundle.FlowInputField{}
	for k, t := range pairs {
		d[k] = bundle.FlowInputField{Type: t}
	}
	return d
}

// The real SDK→binary boundary: object/array/number/bool inputs arrive as JSON *strings*
// (SDK json.dumps → CLI stores verbatim). Coercion must restore their declared types.
func TestCoerceDeclaredInputs_DecodesByDeclaredType(t *testing.T) {
	inputs := map[string]any{
		"prior_run":    `{"cast_analyze": {"output": {"plan": "x"}}, "_changed": {}}`,
		"routing_bias": `[]`,
		"batch_size":   `20000`,
		"flag":         `false`,
		"notes":        "region=India, currency=INR", // declared string — must stay verbatim
		"as_of_date":   "2026-07-26",                  // plain string that isn't JSON
	}
	out := coerceDeclaredInputs(inputs, decl(map[string]string{
		"prior_run":    "object",
		"routing_bias": "array",
		"batch_size":   "number",
		"flag":         "boolean",
		"notes":        "string",
		"as_of_date":   "string",
	}))

	pr, ok := out["prior_run"].(map[string]any)
	if !ok {
		t.Fatalf("prior_run should decode to a map, got %T", out["prior_run"])
	}
	// the whole point: a nested slice is now indexable
	if _, ok := pr["cast_analyze"].(map[string]any); !ok {
		t.Errorf("prior_run.cast_analyze should be a map, got %T", pr["cast_analyze"])
	}
	if arr, ok := out["routing_bias"].([]any); !ok || len(arr) != 0 {
		t.Errorf("routing_bias should decode to empty slice, got %T %v", out["routing_bias"], out["routing_bias"])
	}
	if n, ok := out["batch_size"].(float64); !ok || n != 20000 {
		t.Errorf("batch_size should decode to 20000, got %T %v", out["batch_size"], out["batch_size"])
	}
	if b, ok := out["flag"].(bool); !ok || b != false {
		t.Errorf("flag should decode to false bool, got %T %v", out["flag"], out["flag"])
	}
	if out["notes"] != "region=India, currency=INR" {
		t.Errorf("declared-string input must stay verbatim, got %v", out["notes"])
	}
	if out["as_of_date"] != "2026-07-26" {
		t.Errorf("plain string must stay verbatim, got %v", out["as_of_date"])
	}
}

// End-to-end: a nested binding that FAILS on a JSON-string parent must SUCCEED once coerced.
// This is the exact bug — the pre-existing optional_binding_test used a native map, so it
// never exercised the string boundary and passed while production silently dropped every prior.
func TestCoerceThenResolveNestedBinding(t *testing.T) {
	priorJSON := `{"cast_analyze": {"input": {"a": 1}, "output": {"plan": "x"}}, "_changed": {"batch_size": {"prior": 10, "current": 5}}}`

	// Before coercion: nested path into a string yields nothing (optional → null).
	rawCtx := NewExecutionContext(map[string]any{"prior_run": priorJSON})
	node := bundle.Node{Inputs: map[string]bundle.InputBinding{
		"prior":   {From: "$.inputs.prior_run.cast_analyze", Optional: true},
		"changes": {From: "$.inputs.prior_run._changed", Optional: true},
	}}
	before, err := resolveNodeInputs(node, rawCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if before["prior"] != nil {
		t.Errorf("precondition: nested path into a JSON string should NOT resolve, got %v", before["prior"])
	}

	// After coercion: the same binding resolves the real slice + _changed.
	coerced := coerceDeclaredInputs(map[string]any{"prior_run": priorJSON}, decl(map[string]string{"prior_run": "object"}))
	ctx := NewExecutionContext(coerced)
	after, err := resolveNodeInputs(node, ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, ok := after["prior"].(map[string]any)
	if !ok || got["output"] == nil {
		t.Fatalf("after coercion, prior should be the real slice, got %T %v", after["prior"], after["prior"])
	}
	if _, ok := after["changes"].(map[string]any); !ok {
		t.Errorf("after coercion, changes (_changed) should resolve, got %T", after["changes"])
	}
}

func TestCoerceDeclaredInputs_LeavesUntouched(t *testing.T) {
	// invalid JSON for a structured type → keep the string (best-effort, never error)
	got := coerceDeclaredInputs(map[string]any{"x": "{not json"}, decl(map[string]string{"x": "object"}))
	if got["x"] != "{not json" {
		t.Errorf("undecodable value should stay a string, got %v", got["x"])
	}
	// already-native (not a string) → untouched, no double-decode
	native := map[string]any{"cast_analyze": map[string]any{}}
	got = coerceDeclaredInputs(map[string]any{"prior_run": native}, decl(map[string]string{"prior_run": "object"}))
	if _, ok := got["prior_run"].(map[string]any); !ok {
		t.Errorf("already-native value should pass through, got %T", got["prior_run"])
	}
	// undeclared input → untouched (e.g. company_dna_unavailable, not in flow.Inputs)
	got = coerceDeclaredInputs(map[string]any{"undeclared": "false"}, decl(map[string]string{}))
	if got["undeclared"] != "false" {
		t.Errorf("undeclared input must be left as-is, got %v", got["undeclared"])
	}
}
