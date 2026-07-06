package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
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

func TestResolveNodeInputs_FilePathBinding(t *testing.T) {
	reg := NewRegistry()
	reg.Register("echo@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, inputs map[string]any) (map[string]any, error) {
		return inputs, nil
	}))

	t.Run("file_path loads PNG as FileValue", func(t *testing.T) {
		dir := t.TempDir()
		pngData := []byte{
			0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
			0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
		}
		imgPath := filepath.Join(dir, "crop.png")
		if err := os.WriteFile(imgPath, pngData, 0o644); err != nil {
			t.Fatal(err)
		}

		node := makeNode(t, `{
			"type": "tool_call",
			"description": "test",
			"inputs": {
				"img": { "from": "$.inputs.path", "type": "file_path" }
			},
			"config": { "tool": "echo@v1", "args": {} }
		}`)

		// Test resolveNodeInputs directly: the resolved map holds a FileValue.
		// (ExecuteToolCall passes rendered args — not the resolved map — to the tool,
		// so we verify the binding-resolution layer here rather than through the tool output.)
		execCtx := NewExecutionContext(map[string]any{"path": imgPath})
		resolved, err := resolveNodeInputs(node, execCtx)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		fv, ok := resolved["img"].(FileValue)
		if !ok {
			t.Fatalf("expected FileValue in resolved map, got %T", resolved["img"])
		}
		if fv.MediaType != "image/png" {
			t.Errorf("got MediaType=%q, want \"image/png\"", fv.MediaType)
		}
		if fv.Name != "crop.png" {
			t.Errorf("got Name=%q, want \"crop.png\"", fv.Name)
		}
		if len(fv.Data) != len(pngData) {
			t.Errorf("got %d bytes, want %d", len(fv.Data), len(pngData))
		}
	})

	t.Run("missing file returns error", func(t *testing.T) {
		dir := t.TempDir()
		missingPath := filepath.Join(dir, "nonexistent.png")

		node := makeNode(t, `{
			"type": "tool_call",
			"description": "test",
			"inputs": {
				"img": { "from": "$.inputs.path", "type": "file_path" }
			},
			"config": { "tool": "echo@v1", "args": {} }
		}`)

		execCtx := NewExecutionContext(map[string]any{"path": missingPath})
		_, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
		if err == nil {
			t.Fatal("expected error for missing file, got nil")
		}
		if !strings.Contains(err.Error(), missingPath) {
			t.Errorf("error should contain missing path %q, got: %v", missingPath, err)
		}
	})

	t.Run("non-string resolved value returns error", func(t *testing.T) {
		node := makeNode(t, `{
			"type": "tool_call",
			"description": "test",
			"inputs": {
				"img": { "from": "$.inputs.not_a_path", "type": "file_path" }
			},
			"config": { "tool": "echo@v1", "args": {} }
		}`)

		execCtx := NewExecutionContext(map[string]any{"not_a_path": 42})
		_, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
		if err == nil {
			t.Fatal("expected error for non-string value, got nil")
		}
	})

	t.Run("empty type is backward compatible", func(t *testing.T) {
		node := makeNode(t, `{
			"type": "tool_call",
			"description": "test",
			"inputs": {
				"val": { "from": "$.inputs.plain" }
			},
			"config": { "tool": "echo@v1", "args": { "v": "{{ val }}" } }
		}`)

		execCtx := NewExecutionContext(map[string]any{"plain": "hello"})
		out, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if out["v"] != "hello" {
			t.Errorf("got v=%v, want hello", out["v"])
		}
	})
}

// --- Gap 1: file_path_array binding ---

func TestResolveNodeInputs_FilePathArray(t *testing.T) {
	dir := t.TempDir()
	pngMagic := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}

	path1 := filepath.Join(dir, "front.png")
	path2 := filepath.Join(dir, "top.png")
	for _, p := range []string{path1, path2} {
		if err := os.WriteFile(p, pngMagic, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	node := makeNode(t, `{
		"type": "prompt",
		"inputs": {
			"crops": { "from": "$.inputs.paths", "type": "file_path_array" }
		},
		"config": {"model": "stub/stub", "user": "analyze"}
	}`)

	execCtx := NewExecutionContext(map[string]any{
		"paths": []any{path1, path2},
	})

	resolved, err := resolveNodeInputs(node, execCtx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	fvs, ok := resolved["crops"].([]FileValue)
	if !ok {
		t.Fatalf("expected []FileValue, got %T", resolved["crops"])
	}
	if len(fvs) != 2 {
		t.Fatalf("expected 2 FileValues, got %d", len(fvs))
	}
	for i, fv := range fvs {
		if fv.MediaType != "image/png" {
			t.Errorf("fvs[%d]: expected image/png, got %q", i, fv.MediaType)
		}
	}
}

func TestResolveNodeInputs_FilePathArray_NonArrayError(t *testing.T) {
	node := makeNode(t, `{
		"type": "prompt",
		"inputs": {
			"crops": { "from": "$.inputs.not_array", "type": "file_path_array" }
		},
		"config": {"model": "stub/stub", "user": "analyze"}
	}`)

	execCtx := NewExecutionContext(map[string]any{"not_array": "single_string"})
	_, err := resolveNodeInputs(node, execCtx)
	if err == nil {
		t.Error("expected error for non-array value with file_path_array binding")
	}
}

// --- Gap 4: tool timeout from signature ---

func TestExecuteToolCall_TimeoutFromSignature(t *testing.T) {
	reg := NewRegistry()
	// Register a tool that checks whether its context has a deadline.
	deadlineTool := ToolFunc(func(ctx context.Context, _ map[string]any) (map[string]any, error) {
		_, hasDeadline := ctx.Deadline()
		return map[string]any{"has_deadline": hasDeadline}, nil
	})
	sig := bundle.ToolSignature{TimeoutSeconds: 60}
	if err := reg.Register("deadline_check@v1", sig, deadlineTool); err != nil {
		t.Fatal(err)
	}

	node := makeNode(t, `{
		"type": "tool_call",
		"inputs": {},
		"config": {"tool": "deadline_check@v1"}
	}`)

	execCtx := NewExecutionContext(map[string]any{})
	out, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["has_deadline"] != true {
		t.Errorf("expected context deadline to be set when TimeoutSeconds > 0, got: %v", out["has_deadline"])
	}
}

func TestExecuteToolCall_NoTimeoutWhenZero(t *testing.T) {
	reg := NewRegistry()
	deadlineTool := ToolFunc(func(ctx context.Context, _ map[string]any) (map[string]any, error) {
		_, hasDeadline := ctx.Deadline()
		return map[string]any{"has_deadline": hasDeadline}, nil
	})
	sig := bundle.ToolSignature{TimeoutSeconds: 0} // no timeout
	if err := reg.Register("nodeadline@v1", sig, deadlineTool); err != nil {
		t.Fatal(err)
	}

	node := makeNode(t, `{
		"type": "tool_call",
		"inputs": {},
		"config": {"tool": "nodeadline@v1"}
	}`)

	execCtx := NewExecutionContext(map[string]any{})
	out, err := ExecuteToolCall(context.Background(), node, execCtx, reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["has_deadline"] != false {
		t.Errorf("expected no context deadline when TimeoutSeconds == 0, got: %v", out["has_deadline"])
	}
}
