package runtime

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/aditya-vinodh/agent-runtime/internal/bundle"
)

func makeNode(t *testing.T, nodeJSON string) bundle.Node {
	t.Helper()
	var n bundle.Node
	if err := json.Unmarshal([]byte(nodeJSON), &n); err != nil {
		t.Fatalf("invalid node JSON: %v", err)
	}
	return n
}

func TestExecuteToolCall(t *testing.T) {
	reg := NewRegistry()
	sig := bundle.ToolSignature{}

	// Tool that echoes its inputs as outputs.
	echoTool := ToolFunc(func(_ context.Context, inputs map[string]any) (map[string]any, error) {
		return inputs, nil
	})
	if err := reg.Register("echo@v1", sig, echoTool); err != nil {
		t.Fatal(err)
	}

	t.Run("resolves inputs and renders args", func(t *testing.T) {
		node := makeNode(t, `{
			"type": "tool_call",
			"description": "test",
			"inputs": {
				"part": { "from": "$.inputs.part_number" }
			},
			"config": {
				"tool": "echo@v1",
				"args": { "part_number": "{{ part }}" }
			}
		}`)

		execCtx := NewExecutionContext(map[string]any{"part_number": "ABC-123"})
		out, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out["part_number"] != "ABC-123" {
			t.Errorf("got part_number=%v, want ABC-123", out["part_number"])
		}
	})

	t.Run("non-string arg passes through", func(t *testing.T) {
		node := makeNode(t, `{
			"type": "tool_call",
			"description": "test",
			"inputs": {},
			"config": {
				"tool": "echo@v1",
				"args": { "count": 5 }
			}
		}`)

		execCtx := NewExecutionContext(map[string]any{})
		out, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// JSON numbers unmarshal as float64.
		if out["count"] != float64(5) {
			t.Errorf("got count=%v (%T), want 5", out["count"], out["count"])
		}
	})

	t.Run("no args field is fine", func(t *testing.T) {
		node := makeNode(t, `{
			"type": "tool_call",
			"description": "test",
			"inputs": {},
			"config": { "tool": "echo@v1" }
		}`)

		execCtx := NewExecutionContext(map[string]any{})
		out, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(out) != 0 {
			t.Errorf("expected empty output, got %v", out)
		}
	})

	t.Run("missing tool in registry", func(t *testing.T) {
		node := makeNode(t, `{
			"type": "tool_call",
			"description": "test",
			"inputs": {},
			"config": { "tool": "missing@v1" }
		}`)

		execCtx := NewExecutionContext(map[string]any{})
		_, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
		if err == nil {
			t.Error("expected error for unregistered tool, got nil")
		}
	})

	t.Run("unresolvable input binding", func(t *testing.T) {
		node := makeNode(t, `{
			"type": "tool_call",
			"description": "test",
			"inputs": {
				"x": { "from": "$.inputs.missing_field" }
			},
			"config": { "tool": "echo@v1", "args": {} }
		}`)

		execCtx := NewExecutionContext(map[string]any{})
		_, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
		if err == nil {
			t.Error("expected error for missing input binding, got nil")
		}
	})

	t.Run("undefined template placeholder in arg", func(t *testing.T) {
		node := makeNode(t, `{
			"type": "tool_call",
			"description": "test",
			"inputs": {},
			"config": {
				"tool": "echo@v1",
				"args": { "k": "{{ undefined }}" }
			}
		}`)

		execCtx := NewExecutionContext(map[string]any{})
		_, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
		if err == nil {
			t.Error("expected error for undefined template placeholder, got nil")
		}
	})
}
