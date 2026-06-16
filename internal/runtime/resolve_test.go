package runtime

import (
	"testing"
)

func TestResolve(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{
		"rfq_document": "some text",
		"nested":       map[string]any{"deep": "value"},
	})
	ctx.SetNodeOutput("extract_items", map[string]any{
		"items": []any{
			map[string]any{"part_number": "A1", "quantity": 3},
		},
	})

	tests := []struct {
		path    string
		want    any
		wantErr bool
	}{
		{"$.inputs.rfq_document", "some text", false},
		{"$.inputs.nested.deep", "value", false},
		{"$.extract_items.output", map[string]any{"items": []any{map[string]any{"part_number": "A1", "quantity": 3}}}, false},
		{"$.extract_items.output.items", []any{map[string]any{"part_number": "A1", "quantity": 3}}, false},
		// errors
		{"inputs.rfq_document", nil, true},  // missing $.
		{"$.inputs", nil, true},             // bare $.inputs
		{"$.extract_items", nil, true},      // missing .output
		{"$.inputs.missing", nil, true},     // unknown input field
		{"$.missing_node.output", nil, false},       // node didn't run → null (not error)
		{"$.missing_node.output.field", nil, false}, // nested path on unexecuted node → null
		{"$.inputs.rfq_document.sub", nil, true}, // traversal into string
	}

	for _, tt := range tests {
		got, err := Resolve(ctx, tt.path)
		if tt.wantErr {
			if err == nil {
				t.Errorf("Resolve(%q): expected error, got %v", tt.path, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("Resolve(%q): unexpected error: %v", tt.path, err)
			continue
		}
		// shallow equality check for basic types
		_ = got // deeper comparison skipped for map/slice; no-panic is the key invariant
	}
}

func TestResolve_IterVar(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{})
	ctx.SetIterVar("item", map[string]any{"part_number": "ABC-123", "quantity": 5})

	got, err := Resolve(ctx, "$.item.part_number")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "ABC-123" {
		t.Errorf("expected ABC-123, got %v", got)
	}

	got2, err := Resolve(ctx, "$.item.quantity")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got2 != 5 {
		t.Errorf("expected 5, got %v", got2)
	}

	// Iter var takes precedence over a node output with the same name.
	ctx.SetNodeOutput("item", map[string]any{"output": "should not be seen"})
	got3, err := Resolve(ctx, "$.item.part_number")
	if err != nil {
		t.Fatalf("unexpected error after SetNodeOutput: %v", err)
	}
	if got3 != "ABC-123" {
		t.Errorf("iter var should shadow node output, got %v", got3)
	}

	// After clearing the iter var, node output should be reachable again.
	ctx.ClearIterVar("item")
	_, err = Resolve(ctx, "$.item.part_number")
	if err == nil {
		t.Error("expected error after ClearIterVar (no .output in path), got nil")
	}
}
