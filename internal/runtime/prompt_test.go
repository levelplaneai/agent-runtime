package runtime

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestResolveText_Inline(t *testing.T) {
	config := map[string]json.RawMessage{
		"system": json.RawMessage(`"You are a helpful assistant."`),
	}
	got, err := resolveText(config, "system", "/nonexistent/system.prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "You are a helpful assistant." {
		t.Errorf("got %q, want inline value", got)
	}
}

func TestResolveText_File(t *testing.T) {
	dir := t.TempDir()
	content := "You are an extractor.\n"
	if err := os.WriteFile(filepath.Join(dir, "system.prompt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	config := map[string]json.RawMessage{}
	got, err := resolveText(config, "system", filepath.Join(dir, "system.prompt"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != content {
		t.Errorf("got %q, want file content", got)
	}
}

func TestResolveText_Missing(t *testing.T) {
	got, err := resolveText(map[string]json.RawMessage{}, "system", "/nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestBuildMessages_InlineUser(t *testing.T) {
	config := map[string]json.RawMessage{
		"user": json.RawMessage(`"Hello, {{ name }}!"`),
	}
	msgs, err := buildMessages(config, "/unused", map[string]any{"name": "World"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Role != "user" {
		t.Errorf("expected role user, got %q", msgs[0].Role)
	}
	if msgs[0].Content != "Hello, World!" {
		t.Errorf("unexpected content: %q", msgs[0].Content)
	}
}

func TestBuildMessages_FileUser(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user.prompt"), []byte("Process: {{ doc }}"), 0o644); err != nil {
		t.Fatal(err)
	}
	config := map[string]json.RawMessage{}
	msgs, err := buildMessages(config, dir, map[string]any{"doc": "my doc"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
}

func TestBuildMessages_MultiTurn(t *testing.T) {
	turns := []map[string]string{
		{"role": "user", "content": "Hello {{ x }}"},
		{"role": "assistant", "content": "Hi there"},
		{"role": "user", "content": "Follow up"},
	}
	raw, _ := json.Marshal(turns)
	config := map[string]json.RawMessage{"messages": raw}
	msgs, err := buildMessages(config, "/unused", map[string]any{"x": "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "user" || msgs[1].Role != "assistant" {
		t.Errorf("unexpected roles: %v", msgs)
	}
}

func TestBuildMessages_MissingUser(t *testing.T) {
	_, err := buildMessages(map[string]json.RawMessage{}, "/nonexistent", map[string]any{})
	if err == nil {
		t.Error("expected error when no user message source, got nil")
	}
}

func TestRawSchemaToMap(t *testing.T) {
	raw := map[string]json.RawMessage{
		"type":       json.RawMessage(`"object"`),
		"properties": json.RawMessage(`{"name":{"type":"string"}}`),
		"required":   json.RawMessage(`["name"]`),
	}
	got, err := rawSchemaToMap(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got["type"] != "object" {
		t.Errorf("expected type=object, got %v", got["type"])
	}
	if got["required"] == nil {
		t.Error("required field missing")
	}
}

func TestBuildCompletionRequest_SystemAndUser(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user.prompt"), []byte("Process: {{ doc }}"), 0o644); err != nil {
		t.Fatal(err)
	}

	node := makeNode(t, `{
		"type": "prompt",
		"description": "test",
		"inputs": {"doc": {"from": "$.inputs.document"}},
		"config": {
			"model": "anthropic/claude-haiku-4-5",
			"system": "You are a test assistant."
		},
		"output_schema": {
			"type": "object",
			"properties": {"result": {"type": "string"}},
			"required": ["result"]
		}
	}`)

	req, err := buildCompletionRequest(node, dir, map[string]any{"doc": "hello world"}, "anthropic/claude-haiku-4-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.System != "You are a test assistant." {
		t.Errorf("unexpected system: %q", req.System)
	}
	if len(req.Messages) != 1 {
		t.Errorf("expected 1 message, got %d", len(req.Messages))
	}
	if req.OutputSchema == nil {
		t.Error("expected OutputSchema to be set")
	}
	if req.Model != "anthropic/claude-haiku-4-5" {
		t.Errorf("unexpected model: %q", req.Model)
	}
}

// stubProvider is a test double for LLMProvider.
type stubProvider struct {
	response CompletionResponse
	err      error
}

func (s *stubProvider) Complete(_ context.Context, _ CompletionRequest) (CompletionResponse, error) {
	return s.response, s.err
}

func TestExecutePrompt_JSONOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user.prompt"), []byte("say hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	node := makeNode(t, `{
		"type": "prompt",
		"description": "test",
		"inputs": {},
		"config": {"model": "openai/gpt-4o"}
	}`)

	provider := &stubProvider{response: CompletionResponse{Content: `{"text":"hello"}`}}
	execCtx := NewExecutionContext(map[string]any{})

	out, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["text"] != "hello" {
		t.Errorf("unexpected output: %v", out)
	}
}

func TestExecutePrompt_PlainTextOutput(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user.prompt"), []byte("say hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	node := makeNode(t, `{
		"type": "prompt",
		"description": "test",
		"inputs": {},
		"config": {"model": "gemini/gemini-2.0-flash"}
	}`)

	provider := &stubProvider{response: CompletionResponse{Content: "hello world"}}
	execCtx := NewExecutionContext(map[string]any{})

	out, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["text"] != "hello world" {
		t.Errorf("unexpected output: %v", out)
	}
}
