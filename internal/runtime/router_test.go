package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// mockProvider is a test double for LLMProvider that returns a fixed response.
type mockProvider struct {
	response string
	err      error
}

func (m *mockProvider) Complete(_ context.Context, _ CompletionRequest) (CompletionResponse, error) {
	if m.err != nil {
		return CompletionResponse{}, m.err
	}
	return CompletionResponse{Content: m.response}, nil
}

func TestExecuteRouter_DefaultBranch(t *testing.T) {
	node := bundle.Node{
		Type: "router",
		Config: map[string]json.RawMessage{
			"branches": json.RawMessage(`[{"default":true,"goto":"target_node"}]`),
		},
	}
	execCtx := NewExecutionContext(map[string]any{})

	out, err := ExecuteRouter(context.Background(), "test_router", node, "", execCtx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["_goto"] != "target_node" {
		t.Errorf("expected _goto=target_node, got %v", out["_goto"])
	}
}

func TestExecuteRouter_WhenLength(t *testing.T) {
	node := bundle.Node{
		Type: "router",
		Inputs: map[string]bundle.InputBinding{
			"items": {From: "$.prev.output.items"},
		},
		Config: map[string]json.RawMessage{
			"branches": json.RawMessage(`[
				{"when":"$.inputs.items.length == 0","goto":"empty_branch"},
				{"default":true,"goto":"non_empty_branch"}
			]`),
		},
	}

	// empty array → first branch
	ctx1 := NewExecutionContext(map[string]any{})
	ctx1.SetNodeOutput("prev", map[string]any{"items": []any{}})
	out1, err := ExecuteRouter(context.Background(), "test_router", node, "", ctx1, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out1["_goto"] != "empty_branch" {
		t.Errorf("expected empty_branch, got %v", out1["_goto"])
	}

	// non-empty array → default branch
	ctx2 := NewExecutionContext(map[string]any{})
	ctx2.SetNodeOutput("prev", map[string]any{"items": []any{"x", "y"}})
	out2, err := ExecuteRouter(context.Background(), "test_router", node, "", ctx2, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out2["_goto"] != "non_empty_branch" {
		t.Errorf("expected non_empty_branch, got %v", out2["_goto"])
	}
}

func TestExecuteRouter_WhenStringEquality(t *testing.T) {
	node := bundle.Node{
		Type: "router",
		Inputs: map[string]bundle.InputBinding{
			"decision": {From: "$.llm.output.decision"},
		},
		Config: map[string]json.RawMessage{
			"branches": json.RawMessage(`[
				{"when":"$.decision == 'approve'","goto":"approve_branch"},
				{"default":true,"goto":"reject_branch"}
			]`),
		},
	}

	ctx := NewExecutionContext(map[string]any{})
	ctx.SetNodeOutput("llm", map[string]any{"decision": "approve"})

	out, err := ExecuteRouter(context.Background(), "test_router", node, "", ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["_goto"] != "approve_branch" {
		t.Errorf("expected approve_branch, got %v", out["_goto"])
	}
}

func TestExecuteRouter_NoMatch(t *testing.T) {
	node := bundle.Node{
		Type: "router",
		Inputs: map[string]bundle.InputBinding{
			"decision": {From: "$.llm.output.decision"},
		},
		Config: map[string]json.RawMessage{
			"branches": json.RawMessage(`[
				{"when":"$.decision == 'approve'","goto":"approve_branch"}
			]`),
		},
	}

	ctx := NewExecutionContext(map[string]any{})
	ctx.SetNodeOutput("llm", map[string]any{"decision": "reject"})

	_, err := ExecuteRouter(context.Background(), "test_router", node, "", ctx, nil)
	if err == nil {
		t.Error("expected error when no branch matches, got nil")
	}
}

func TestExecuteRouter_LLMDecision_Success(t *testing.T) {
	node := bundle.Node{
		Type: "router",
		Inputs: map[string]bundle.InputBinding{
			"rfq": {From: "$.inputs.rfq_document"},
		},
		Config: map[string]json.RawMessage{
			"decide_with": json.RawMessage(`{
				"model": "anthropic/claude-haiku-4-5",
				"prompt": "Classify this RFQ: {{ rfq }}",
				"choices": ["standard", "custom_machining", "assembly", "unclear"]
			}`),
			"branches": json.RawMessage(`[
				{"when":"$.decision == 'standard'","goto":"standard_flow"},
				{"when":"$.decision == 'custom_machining'","goto":"machining_flow"},
				{"when":"$.decision == 'assembly'","goto":"assembly_flow"},
				{"when":"$.decision == 'unclear'","goto":"request_clarification"}
			]`),
		},
	}

	execCtx := NewExecutionContext(map[string]any{"rfq_document": "standard bolt order"})
	provider := &mockProvider{response: `{"decision":"standard"}`}

	out, err := ExecuteRouter(context.Background(), "classify", node, "", execCtx, provider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["_goto"] != "standard_flow" {
		t.Errorf("expected standard_flow, got %v", out["_goto"])
	}
}

func TestExecuteRouter_LLMDecision_InvalidChoice(t *testing.T) {
	node := bundle.Node{
		Type: "router",
		Inputs: map[string]bundle.InputBinding{
			"rfq": {From: "$.inputs.rfq_document"},
		},
		Config: map[string]json.RawMessage{
			"decide_with": json.RawMessage(`{
				"model": "anthropic/claude-haiku-4-5",
				"prompt": "Classify: {{ rfq }}",
				"choices": ["standard", "custom"]
			}`),
			"branches": json.RawMessage(`[
				{"when":"$.decision == 'standard'","goto":"standard_flow"},
				{"default":true,"goto":"custom_flow"}
			]`),
		},
	}

	execCtx := NewExecutionContext(map[string]any{"rfq_document": "some rfq"})
	// Model returns a choice not in the declared list.
	provider := &mockProvider{response: `{"decision":"unknown_type"}`}

	_, err := ExecuteRouter(context.Background(), "classify", node, "", execCtx, provider)
	if err == nil {
		t.Error("expected error for invalid choice, got nil")
	}
}

func TestExecuteRouter_LLMDecision_FilePrompt(t *testing.T) {
	dir := t.TempDir()
	promptFile := filepath.Join(dir, "classify.md")
	if err := os.WriteFile(promptFile, []byte("Classify this document: {{ doc }}"), 0644); err != nil {
		t.Fatalf("writing prompt file: %v", err)
	}

	node := bundle.Node{
		Type: "router",
		Inputs: map[string]bundle.InputBinding{
			"doc": {From: "$.inputs.document"},
		},
		Config: map[string]json.RawMessage{
			"decide_with": json.RawMessage(`{
				"model": "anthropic/claude-haiku-4-5",
				"prompt": "./classify.md",
				"choices": ["legal", "technical", "financial"]
			}`),
			"branches": json.RawMessage(`[
				{"when":"$.decision == 'legal'","goto":"legal_flow"},
				{"when":"$.decision == 'technical'","goto":"tech_flow"},
				{"when":"$.decision == 'financial'","goto":"fin_flow"}
			]`),
		},
	}

	execCtx := NewExecutionContext(map[string]any{"document": "contract terms"})

	var capturedRequest CompletionRequest
	capturingProvider := &capturingMockProvider{
		response: `{"decision":"legal"}`,
		capture:  &capturedRequest,
	}

	out, err := ExecuteRouter(context.Background(), "classify", node, dir, execCtx, capturingProvider)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["_goto"] != "legal_flow" {
		t.Errorf("expected legal_flow, got %v", out["_goto"])
	}
	// Verify the prompt file content was loaded and the template was rendered.
	if len(capturedRequest.Messages) == 0 {
		t.Fatal("expected at least one message in the request")
	}
	got := capturedRequest.Messages[0].Content
	want := "Classify this document: contract terms"
	if got != want {
		t.Errorf("rendered prompt: got %q, want %q", got, want)
	}
}

func TestExecuteRouter_LLMDecision_ProviderError(t *testing.T) {
	node := bundle.Node{
		Type: "router",
		Inputs: map[string]bundle.InputBinding{
			"rfq": {From: "$.inputs.rfq_document"},
		},
		Config: map[string]json.RawMessage{
			"decide_with": json.RawMessage(`{
				"model": "anthropic/claude-haiku-4-5",
				"prompt": "Classify: {{ rfq }}",
				"choices": ["standard", "custom"]
			}`),
			"branches": json.RawMessage(`[{"default":true,"goto":"fallback"}]`),
		},
	}

	execCtx := NewExecutionContext(map[string]any{"rfq_document": "test"})
	provider := &mockProvider{err: fmt.Errorf("API timeout")}

	_, err := ExecuteRouter(context.Background(), "classify", node, "", execCtx, provider)
	if err == nil {
		t.Error("expected error on provider failure, got nil")
	}
}

// capturingMockProvider records the last CompletionRequest it received.
type capturingMockProvider struct {
	response string
	capture  *CompletionRequest
}

func (c *capturingMockProvider) Complete(_ context.Context, req CompletionRequest) (CompletionResponse, error) {
	*c.capture = req
	return CompletionResponse{Content: c.response}, nil
}
