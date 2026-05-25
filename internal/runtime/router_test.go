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
				{"when":{"field":"items","op":"length_eq","value":0},"goto":"empty_branch"},
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
				{"when":{"field":"decision","op":"eq","value":"approve"},"goto":"approve_branch"},
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
				{"when":{"field":"decision","op":"eq","value":"approve"},"goto":"approve_branch"}
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
				{"when":{"field":"decision","op":"eq","value":"standard"},"goto":"standard_flow"},
				{"when":{"field":"decision","op":"eq","value":"custom_machining"},"goto":"machining_flow"},
				{"when":{"field":"decision","op":"eq","value":"assembly"},"goto":"assembly_flow"},
				{"when":{"field":"decision","op":"eq","value":"unclear"},"goto":"request_clarification"}
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
				{"when":{"field":"decision","op":"eq","value":"standard"},"goto":"standard_flow"},
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
				{"when":{"field":"decision","op":"eq","value":"legal"},"goto":"legal_flow"},
				{"when":{"field":"decision","op":"eq","value":"technical"},"goto":"tech_flow"},
				{"when":{"field":"decision","op":"eq","value":"financial"},"goto":"fin_flow"}
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

// --- evalCondition unit tests ---

func c(field, op string, value json.RawMessage) *Condition {
	return &Condition{Field: field, Op: op, Value: value}
}

func TestEvalCondition_Eq_String(t *testing.T) {
	resolved := map[string]any{"status": "approved"}
	match, err := evalCondition(c("status", "eq", json.RawMessage(`"approved"`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("status", "eq", json.RawMessage(`"rejected"`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_Eq_Number(t *testing.T) {
	// float64 from JSON unmarshal
	resolved := map[string]any{"score": float64(42)}
	match, err := evalCondition(c("score", "eq", json.RawMessage(`42`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match, got match=%v err=%v", match, err)
	}

	// int-typed Go value (exercises toFloat64 coercion)
	resolved2 := map[string]any{"count": int(5)}
	match2, err2 := evalCondition(c("count", "eq", json.RawMessage(`5`)), resolved2)
	if err2 != nil || !match2 {
		t.Errorf("expected match for int field, got match=%v err=%v", match2, err2)
	}
}

func TestEvalCondition_Eq_Bool(t *testing.T) {
	resolved := map[string]any{"flag": true}
	match, err := evalCondition(c("flag", "eq", json.RawMessage(`true`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("flag", "eq", json.RawMessage(`false`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_Eq_Null(t *testing.T) {
	// absent field matches null
	resolved := map[string]any{}
	match, err := evalCondition(c("missing", "eq", json.RawMessage(`null`)), resolved)
	if err != nil || !match {
		t.Errorf("expected absent field to match null, got match=%v err=%v", match, err)
	}

	// nil value matches null
	resolved2 := map[string]any{"val": nil}
	match2, err2 := evalCondition(c("val", "eq", json.RawMessage(`null`)), resolved2)
	if err2 != nil || !match2 {
		t.Errorf("expected nil field to match null, got match=%v err=%v", match2, err2)
	}

	// non-nil value does not match null
	resolved3 := map[string]any{"val": "something"}
	match3, err3 := evalCondition(c("val", "eq", json.RawMessage(`null`)), resolved3)
	if err3 != nil || match3 {
		t.Errorf("expected non-nil field to not match null, got match=%v err=%v", match3, err3)
	}
}

func TestEvalCondition_Ne(t *testing.T) {
	resolved := map[string]any{"status": "pending"}
	match, err := evalCondition(c("status", "ne", json.RawMessage(`"approved"`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for ne, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("status", "ne", json.RawMessage(`"pending"`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for ne when equal, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_NumericGt(t *testing.T) {
	resolved := map[string]any{"score": float64(8.5)}
	match, err := evalCondition(c("score", "gt", json.RawMessage(`5`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for gt, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("score", "gt", json.RawMessage(`8.5`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for gt when equal, got match=%v err=%v", match, err)
	}

	// int-typed field value exercises toFloat64
	resolved2 := map[string]any{"retries": int(3)}
	match2, err2 := evalCondition(c("retries", "gt", json.RawMessage(`2`)), resolved2)
	if err2 != nil || !match2 {
		t.Errorf("expected match for int field gt, got match=%v err=%v", match2, err2)
	}
}

func TestEvalCondition_NumericGte(t *testing.T) {
	resolved := map[string]any{"score": float64(5)}
	match, err := evalCondition(c("score", "gte", json.RawMessage(`5`)), resolved)
	if err != nil || !match {
		t.Errorf("expected gte boundary to match, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("score", "gte", json.RawMessage(`6`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for gte when less, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_NumericLt(t *testing.T) {
	resolved := map[string]any{"score": float64(3)}
	match, err := evalCondition(c("score", "lt", json.RawMessage(`5`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for lt, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("score", "lt", json.RawMessage(`3`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for lt when equal, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_NumericLte(t *testing.T) {
	resolved := map[string]any{"score": float64(5)}
	match, err := evalCondition(c("score", "lte", json.RawMessage(`5`)), resolved)
	if err != nil || !match {
		t.Errorf("expected lte boundary to match, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("score", "lte", json.RawMessage(`4`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for lte when greater, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_ContainsString(t *testing.T) {
	resolved := map[string]any{"text": "hello world"}
	match, err := evalCondition(c("text", "contains", json.RawMessage(`"world"`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for contains substring, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("text", "contains", json.RawMessage(`"foo"`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for missing substring, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_ContainsArray(t *testing.T) {
	resolved := map[string]any{"tags": []any{"go", "rust", "python"}}
	match, err := evalCondition(c("tags", "contains", json.RawMessage(`"rust"`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for array contains, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("tags", "contains", json.RawMessage(`"java"`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for absent element, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_ContainsArray_Numeric(t *testing.T) {
	// int-typed elements exercise numeric-aware comparison inside contains
	resolved := map[string]any{"ids": []any{int(1), int(2), int(3)}}
	match, err := evalCondition(c("ids", "contains", json.RawMessage(`2`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for numeric array contains, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_ContainsTypeMismatch(t *testing.T) {
	resolved := map[string]any{"count": float64(5)}
	_, err := evalCondition(c("count", "contains", json.RawMessage(`5`)), resolved)
	if err == nil {
		t.Error("expected error for contains on non-string/non-array field")
	}
}

func TestEvalCondition_In_Strings(t *testing.T) {
	resolved := map[string]any{"region": "us-west"}
	match, err := evalCondition(c("region", "in", json.RawMessage(`["us-east","us-west","eu-west"]`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for in, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("region", "in", json.RawMessage(`["us-east","eu-west"]`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for in when absent, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_In_Numbers(t *testing.T) {
	// int-typed field value exercises toFloat64 coercion against JSON float64 list elements
	resolved := map[string]any{"priority": int(2)}
	match, err := evalCondition(c("priority", "in", json.RawMessage(`[1,2,3]`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for numeric in, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_In_NoMatch(t *testing.T) {
	resolved := map[string]any{"status": "archived"}
	match, err := evalCondition(c("status", "in", json.RawMessage(`["active","pending"]`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_In_NotArray(t *testing.T) {
	resolved := map[string]any{"status": "active"}
	_, err := evalCondition(c("status", "in", json.RawMessage(`"not-an-array"`)), resolved)
	if err == nil {
		t.Error("expected error when in value is not a JSON array")
	}
}

func TestEvalCondition_Exists_Present(t *testing.T) {
	resolved := map[string]any{"token": "abc123"}
	match, err := evalCondition(&Condition{Field: "token", Op: "exists"}, resolved)
	if err != nil || !match {
		t.Errorf("expected match for exists on present field, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_Exists_Absent(t *testing.T) {
	resolved := map[string]any{}
	match, err := evalCondition(&Condition{Field: "token", Op: "exists"}, resolved)
	if err != nil || match {
		t.Errorf("expected no match for exists on absent field, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_Exists_Nil(t *testing.T) {
	resolved := map[string]any{"token": nil}
	match, err := evalCondition(&Condition{Field: "token", Op: "exists"}, resolved)
	if err != nil || match {
		t.Errorf("expected no match for exists on nil field, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_Exists_WithValue_Error(t *testing.T) {
	resolved := map[string]any{"token": "abc"}
	_, err := evalCondition(c("token", "exists", json.RawMessage(`"unexpected"`)), resolved)
	if err == nil {
		t.Error("expected error when exists has a non-null value")
	}
}

func TestEvalCondition_LengthEq_Array(t *testing.T) {
	resolved := map[string]any{"items": []any{"a", "b", "c"}}
	match, err := evalCondition(c("items", "length_eq", json.RawMessage(`3`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for length_eq on array, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("items", "length_eq", json.RawMessage(`2`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for wrong length, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_LengthEq_String(t *testing.T) {
	resolved := map[string]any{"code": "abc"}
	match, err := evalCondition(c("code", "length_eq", json.RawMessage(`3`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for length_eq on string, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_LengthGt(t *testing.T) {
	resolved := map[string]any{"items": []any{"a", "b", "c"}}
	match, err := evalCondition(c("items", "length_gt", json.RawMessage(`2`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for length_gt, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("items", "length_gt", json.RawMessage(`3`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for length_gt when equal, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_LengthLte(t *testing.T) {
	resolved := map[string]any{"items": []any{"a", "b"}}
	match, err := evalCondition(c("items", "length_lte", json.RawMessage(`2`)), resolved)
	if err != nil || !match {
		t.Errorf("expected match for length_lte boundary, got match=%v err=%v", match, err)
	}
	match, err = evalCondition(c("items", "length_lte", json.RawMessage(`1`)), resolved)
	if err != nil || match {
		t.Errorf("expected no match for length_lte when over, got match=%v err=%v", match, err)
	}
}

func TestEvalCondition_Length_Nil(t *testing.T) {
	resolved := map[string]any{"items": nil}
	_, err := evalCondition(c("items", "length_eq", json.RawMessage(`0`)), resolved)
	if err == nil {
		t.Error("expected error for length_* on nil field")
	}
}

func TestEvalCondition_Length_TypeMismatch(t *testing.T) {
	resolved := map[string]any{"count": float64(5)}
	_, err := evalCondition(c("count", "length_eq", json.RawMessage(`1`)), resolved)
	if err == nil {
		t.Error("expected error for length_* on non-array/non-string field")
	}
}

func TestEvalCondition_UnknownOp(t *testing.T) {
	resolved := map[string]any{"x": "y"}
	// parseBranches would catch this earlier, but evalCondition also handles it
	cond := &Condition{Field: "x", Op: "matches_regex", Value: json.RawMessage(`".*"`)}
	_, err := evalCondition(cond, resolved)
	if err == nil {
		t.Error("expected error for unknown operator")
	}
}

func TestEvalCondition_EmptyField(t *testing.T) {
	resolved := map[string]any{}
	_, err := evalCondition(&Condition{Field: "", Op: "eq", Value: json.RawMessage(`"x"`)}, resolved)
	if err == nil {
		t.Error("expected error for empty field name")
	}
}

func TestEvalCondition_FieldAbsent_Numeric(t *testing.T) {
	resolved := map[string]any{}
	_, err := evalCondition(c("score", "gt", json.RawMessage(`5`)), resolved)
	if err == nil {
		t.Error("expected error when numeric field is absent")
	}
}

// --- E2E integration smoke tests ---

func TestExecuteRouter_StructuredWhenLengthEq(t *testing.T) {
	node := bundle.Node{
		Type: "router",
		Inputs: map[string]bundle.InputBinding{
			"results": {From: "$.search.output.results"},
		},
		Config: map[string]json.RawMessage{
			"branches": json.RawMessage(`[
				{"when":{"field":"results","op":"length_eq","value":0},"goto":"no_results"},
				{"default":true,"goto":"has_results"}
			]`),
		},
	}

	ctx := NewExecutionContext(map[string]any{})
	ctx.SetNodeOutput("search", map[string]any{"results": []any{}})

	out, err := ExecuteRouter(context.Background(), "router", node, "", ctx, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["_goto"] != "no_results" {
		t.Errorf("expected no_results, got %v", out["_goto"])
	}
}

func TestExecuteRouter_StructuredWhenEq(t *testing.T) {
	node := bundle.Node{
		Type: "router",
		Inputs: map[string]bundle.InputBinding{
			"verdict": {From: "$.review.output.verdict"},
		},
		Config: map[string]json.RawMessage{
			"branches": json.RawMessage(`[
				{"when":{"field":"verdict","op":"eq","value":"approved"},"goto":"publish"},
				{"when":{"field":"verdict","op":"eq","value":"rejected"},"goto":"archive"},
				{"default":true,"goto":"review_again"}
			]`),
		},
	}

	for _, tc := range []struct {
		verdict string
		want    string
	}{
		{"approved", "publish"},
		{"rejected", "archive"},
		{"pending", "review_again"},
	} {
		ctx := NewExecutionContext(map[string]any{})
		ctx.SetNodeOutput("review", map[string]any{"verdict": tc.verdict})
		out, err := ExecuteRouter(context.Background(), "router", node, "", ctx, nil)
		if err != nil {
			t.Fatalf("verdict=%q: unexpected error: %v", tc.verdict, err)
		}
		if out["_goto"] != tc.want {
			t.Errorf("verdict=%q: expected %q, got %v", tc.verdict, tc.want, out["_goto"])
		}
	}
}
