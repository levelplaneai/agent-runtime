package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// scriptedProvider replays a fixed sequence of CompletionResponses for multi-turn testing.
type scriptedProvider struct {
	t         *testing.T
	responses []CompletionResponse
	calls     []CompletionRequest
	idx       int
}

func (s *scriptedProvider) Complete(_ context.Context, req CompletionRequest) (CompletionResponse, error) {
	s.calls = append(s.calls, req)
	if s.idx >= len(s.responses) {
		s.t.Fatalf("unexpected extra Complete call (call #%d, only %d scripted)", s.idx+1, len(s.responses))
	}
	r := s.responses[s.idx]
	s.idx++
	return r, nil
}

// echoTool returns its inputs back as output.
type echoTool struct{}

func (e echoTool) Call(_ context.Context, inputs map[string]any) (map[string]any, error) {
	return inputs, nil
}

func makeAgentNode(t *testing.T, tools []string, maxIter int, extraConfig string) bundle.Node {
	t.Helper()
	toolsJSON, _ := json.Marshal(tools)
	maxIterJSON := "10"
	if maxIter > 0 {
		b, _ := json.Marshal(maxIter)
		maxIterJSON = string(b)
	}
	nodeJSON := `{
		"type": "prompt",
		"description": "test agent",
		"inputs": {},
		"config": {
			"model": "test/model",
			"user": "do something",
			"tools": ` + string(toolsJSON) + `,
			"max_tool_iterations": ` + maxIterJSON + `
			` + extraConfig + `
		}
	}`
	return makeNode(t, nodeJSON)
}

func makeTestRegistry(t *testing.T, refs ...string) *Registry {
	t.Helper()
	reg := NewRegistry()
	for _, ref := range refs {
		sig := bundle.ToolSignature{
			Description: "test tool " + ref,
			InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"string"}}}`),
		}
		if err := reg.Register(ref, sig, echoTool{}); err != nil {
			t.Fatalf("register %q: %v", ref, err)
		}
	}
	return reg
}

func TestAgenticLoop_OneRound(t *testing.T) {
	dir := t.TempDir()
	node := makeAgentNode(t, []string{"echo@v1"}, 5, "")
	reg := makeTestRegistry(t, "echo@v1")
	execCtx := NewExecutionContext(map[string]any{})

	provider := &scriptedProvider{t: t, responses: []CompletionResponse{
		{
			ToolCalls:  []ToolCall{{ID: "tc1", Name: "echo__v1", Input: json.RawMessage(`{"x":"hello"}`)}},
			StopReason: "tool_use",
		},
		{
			Content:    `{"answer":"done"}`,
			StopReason: "end_turn",
		},
	}}

	out, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, reg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["answer"] != "done" {
		t.Errorf("unexpected output: %v", out)
	}
	if provider.idx != 2 {
		t.Errorf("expected 2 LLM calls, got %d", provider.idx)
	}

	// Second call should have 3 messages: initial user, assistant (tool_use), user (tool_result)
	secondCallMsgs := provider.calls[1].Messages
	if len(secondCallMsgs) != 3 {
		t.Fatalf("expected 3 messages on second call, got %d: %+v", len(secondCallMsgs), secondCallMsgs)
	}
	assistantMsg := secondCallMsgs[1]
	if assistantMsg.Role != "assistant" {
		t.Errorf("expected assistant message at index 1, got %q", assistantMsg.Role)
	}
	if len(assistantMsg.Blocks) == 0 || assistantMsg.Blocks[0].Type != "tool_use" {
		t.Errorf("expected tool_use block in assistant message, got %+v", assistantMsg.Blocks)
	}
	toolResultMsg := secondCallMsgs[2]
	if toolResultMsg.Role != "user" {
		t.Errorf("expected user message at index 2, got %q", toolResultMsg.Role)
	}
	if len(toolResultMsg.Blocks) == 0 || toolResultMsg.Blocks[0].Type != "tool_result" {
		t.Errorf("expected tool_result block in user message, got %+v", toolResultMsg.Blocks)
	}
}

func TestAgenticLoop_MultiRound(t *testing.T) {
	dir := t.TempDir()
	node := makeAgentNode(t, []string{"echo@v1"}, 5, "")
	reg := makeTestRegistry(t, "echo@v1")
	execCtx := NewExecutionContext(map[string]any{})

	provider := &scriptedProvider{t: t, responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "tc1", Name: "echo__v1", Input: json.RawMessage(`{"x":"1"}`)}}, StopReason: "tool_use"},
		{ToolCalls: []ToolCall{{ID: "tc2", Name: "echo__v1", Input: json.RawMessage(`{"x":"2"}`)}}, StopReason: "tool_use"},
		{Content: "final answer", StopReason: "end_turn"},
	}}

	out, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, reg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["text"] != "final answer" {
		t.Errorf("unexpected output: %v", out)
	}
	if provider.idx != 3 {
		t.Errorf("expected 3 LLM calls, got %d", provider.idx)
	}
}

func TestAgenticLoop_MaxIterationsExceeded(t *testing.T) {
	dir := t.TempDir()
	node := makeAgentNode(t, []string{"echo@v1"}, 2, "")
	reg := makeTestRegistry(t, "echo@v1")
	execCtx := NewExecutionContext(map[string]any{})

	provider := &scriptedProvider{t: t, responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "tc1", Name: "echo__v1", Input: json.RawMessage(`{}`)}}, StopReason: "tool_use"},
		{ToolCalls: []ToolCall{{ID: "tc2", Name: "echo__v1", Input: json.RawMessage(`{}`)}}, StopReason: "tool_use"},
	}}

	_, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, reg, nil, nil)
	if err == nil {
		t.Fatal("expected error when max iterations exceeded, got nil")
	}
	if !strings.Contains(err.Error(), "max_tool_iterations") {
		t.Errorf("expected max_tool_iterations in error, got %q", err.Error())
	}
}

func TestAgenticLoop_ToolErrorFedBack(t *testing.T) {
	dir := t.TempDir()
	node := makeAgentNode(t, []string{"fail@v1"}, 5, "")

	reg := NewRegistry()
	sig := bundle.ToolSignature{Description: "always fails", InputSchema: json.RawMessage(`{"type":"object"}`)}
	_ = reg.Register("fail@v1", sig, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return nil, fmt.Errorf("tool execution failed")
	}))

	execCtx := NewExecutionContext(map[string]any{})
	provider := &scriptedProvider{t: t, responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "tc1", Name: "fail__v1", Input: json.RawMessage(`{}`)}}, StopReason: "tool_use"},
		{Content: "I understand the tool failed", StopReason: "end_turn"},
	}}

	out, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, reg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["text"] != "I understand the tool failed" {
		t.Errorf("unexpected output: %v", out)
	}

	// Verify the tool_result block was marked as an error
	secondCallMsgs := provider.calls[1].Messages
	toolResultMsg := secondCallMsgs[len(secondCallMsgs)-1]
	if len(toolResultMsg.Blocks) == 0 || !toolResultMsg.Blocks[0].IsError {
		t.Errorf("expected IsError=true in tool_result block, got %+v", toolResultMsg.Blocks)
	}
}

func TestAgenticLoop_HallucinatedToolName(t *testing.T) {
	dir := t.TempDir()
	node := makeAgentNode(t, []string{"echo@v1"}, 5, "")
	reg := makeTestRegistry(t, "echo@v1")
	execCtx := NewExecutionContext(map[string]any{})

	provider := &scriptedProvider{t: t, responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "tc1", Name: "nonexistent_tool", Input: json.RawMessage(`{}`)}}, StopReason: "tool_use"},
		{Content: "I got an error for the unknown tool", StopReason: "end_turn"},
	}}

	out, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, reg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Second call should receive an is_error tool_result
	secondCallMsgs := provider.calls[1].Messages
	toolResultMsg := secondCallMsgs[len(secondCallMsgs)-1]
	if len(toolResultMsg.Blocks) == 0 || !toolResultMsg.Blocks[0].IsError {
		t.Errorf("expected IsError=true for hallucinated tool, got %+v", toolResultMsg.Blocks)
	}
	_ = out
}

func TestAgenticLoop_ToolsAndOutputSchema(t *testing.T) {
	dir := t.TempDir()
	reg := makeTestRegistry(t, "echo@v1")
	execCtx := NewExecutionContext(map[string]any{})

	nodeJSON := `{
		"type": "prompt",
		"description": "test",
		"inputs": {},
		"config": {
			"model": "test/model",
			"user": "do something",
			"tools": ["echo@v1"]
		},
		"output_schema": {
			"type": "object",
			"properties": {"result": {"type": "string"}},
			"required": ["result"]
		}
	}`
	node := makeNode(t, nodeJSON)
	provider := &scriptedProvider{t: t, responses: nil}

	_, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, reg, nil, nil)
	if err == nil {
		t.Fatal("expected error when tools and output_schema both set")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("expected 'mutually exclusive' in error, got %q", err.Error())
	}
	if provider.idx != 0 {
		t.Error("no LLM calls should have been made")
	}
}

func TestAgenticLoop_NoTools_NilReg(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user.prompt"), []byte("say hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	node := makeNode(t, `{
		"type": "prompt",
		"description": "test",
		"inputs": {},
		"config": {"model": "test/model"}
	}`)

	provider := &scriptedProvider{t: t, responses: []CompletionResponse{
		{Content: "hello", StopReason: "end_turn"},
	}}
	execCtx := NewExecutionContext(map[string]any{})

	out, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["text"] != "hello" {
		t.Errorf("unexpected output: %v", out)
	}
	if provider.idx != 1 {
		t.Errorf("expected exactly 1 LLM call, got %d", provider.idx)
	}
}

func TestParseToolConfig_BuiltinOnly(t *testing.T) {
	config := map[string]json.RawMessage{
		"model": json.RawMessage(`"anthropic/claude-sonnet-4-5"`),
		"tools": json.RawMessage(`["anthropic:web_search"]`),
	}
	defs, builtins, refMap, maxIter, err := parseToolConfig(config, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defs) != 0 {
		t.Errorf("expected 0 defs, got %d", len(defs))
	}
	if len(refMap) != 0 {
		t.Errorf("expected 0 refMap entries, got %d", len(refMap))
	}
	if len(builtins) != 1 || builtins[0].Name != "anthropic:web_search" {
		t.Errorf("expected 1 builtin anthropic:web_search, got %v", builtins)
	}
	if maxIter != 10 {
		t.Errorf("expected default maxIter=10, got %d", maxIter)
	}
}

func TestParseToolConfig_MixedTools(t *testing.T) {
	reg := makeTestRegistry(t, "echo@v1")
	config := map[string]json.RawMessage{
		"tools": json.RawMessage(`["anthropic:web_search","echo@v1"]`),
	}
	defs, builtins, _, _, err := parseToolConfig(config, reg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(defs) != 1 {
		t.Errorf("expected 1 def, got %d", len(defs))
	}
	if len(builtins) != 1 || builtins[0].Name != "anthropic:web_search" {
		t.Errorf("expected 1 builtin anthropic:web_search, got %v", builtins)
	}
}

func TestParseToolConfig_UnknownBuiltin(t *testing.T) {
	config := map[string]json.RawMessage{
		"tools": json.RawMessage(`["bogus:tool"]`),
	}
	_, _, _, _, err := parseToolConfig(config, nil)
	if err == nil {
		t.Fatal("expected error for unknown built-in tool")
	}
	if !strings.Contains(err.Error(), "unknown built-in tool") {
		t.Errorf("expected 'unknown built-in tool' in error, got %q", err.Error())
	}
}

func TestParseToolConfig_OpenAIBuiltin(t *testing.T) {
	config := map[string]json.RawMessage{
		"tools": json.RawMessage(`["openai:code_interpreter"]`),
	}
	_, _, _, _, err := parseToolConfig(config, nil)
	if err == nil {
		t.Fatal("expected error for openai built-in tool")
	}
	if !strings.Contains(err.Error(), "OpenAI") {
		t.Errorf("expected 'OpenAI' in error, got %q", err.Error())
	}
}

func TestBuiltinTool_ProviderMismatch(t *testing.T) {
	dir := t.TempDir()
	node := makeNode(t, `{
		"type": "prompt",
		"description": "test",
		"inputs": {},
		"config": {
			"model": "anthropic/claude-sonnet-4-5",
			"user": "hello",
			"tools": ["gemini:code_execution"]
		}
	}`)
	execCtx := NewExecutionContext(map[string]any{})
	provider := &scriptedProvider{t: t, responses: nil}

	_, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for provider/model mismatch")
	}
	if !strings.Contains(err.Error(), "gemini:code_execution") {
		t.Errorf("expected tool name in error, got %q", err.Error())
	}
	if provider.idx != 0 {
		t.Error("no LLM calls should have been made")
	}
}

func TestParseToolConfig_NilRegistryWithRegistryRef(t *testing.T) {
	config := map[string]json.RawMessage{
		"tools": json.RawMessage(`["echo@v1"]`),
	}
	_, _, _, _, err := parseToolConfig(config, nil)
	if err == nil {
		t.Fatal("expected error when registry is nil and a registry ref is present")
	}
	if !strings.Contains(err.Error(), "requires a registry") {
		t.Errorf("expected 'requires a registry' in error, got %q", err.Error())
	}
}

func TestParseToolConfig_SanitizeCollision(t *testing.T) {
	// "my.tool@v1" and "my_tool@v1" both sanitize to "my_tool__v1".
	reg := makeTestRegistry(t, "my.tool@v1", "my_tool@v1")
	config := map[string]json.RawMessage{
		"tools": json.RawMessage(`["my.tool@v1","my_tool@v1"]`),
	}
	_, _, _, _, err := parseToolConfig(config, reg)
	if err == nil {
		t.Fatal("expected error for colliding sanitized tool names")
	}
	if !strings.Contains(err.Error(), "collision") {
		t.Errorf("expected 'collision' in error, got %q", err.Error())
	}
}

func TestAgenticLoop_ImageToolResult(t *testing.T) {
	dir := t.TempDir()
	node := makeAgentNode(t, []string{"screenshot@v1"}, 5, "")

	reg := NewRegistry()
	imgData := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a} // PNG-like bytes
	sig := bundle.ToolSignature{Description: "takes a screenshot", InputSchema: json.RawMessage(`{"type":"object"}`)}
	_ = reg.Register("screenshot@v1", sig, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{
			"label":  "desktop",
			"image":  ToolImageOutput{Data: imgData, MediaType: "image/png"},
		}, nil
	}))

	execCtx := NewExecutionContext(map[string]any{})
	provider := &scriptedProvider{t: t, responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "tc1", Name: "screenshot__v1", Input: json.RawMessage(`{}`)}}, StopReason: "tool_use"},
		{Content: "I can see the desktop", StopReason: "end_turn"},
	}}

	out, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, reg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["text"] != "I can see the desktop" {
		t.Errorf("unexpected output: %v", out)
	}

	// Verify the tool_result message has SubBlocks with text + image.
	secondCallMsgs := provider.calls[1].Messages
	toolResultMsg := secondCallMsgs[len(secondCallMsgs)-1]
	if len(toolResultMsg.Blocks) == 0 {
		t.Fatal("expected tool_result blocks in message")
	}
	cb := toolResultMsg.Blocks[0]
	if cb.Type != "tool_result" {
		t.Fatalf("expected tool_result block, got %q", cb.Type)
	}
	if len(cb.SubBlocks) != 2 {
		t.Fatalf("expected 2 sub-blocks (text + image), got %d: %+v", len(cb.SubBlocks), cb.SubBlocks)
	}
	if cb.SubBlocks[0].Type != "text" {
		t.Errorf("expected first sub-block type=text, got %q", cb.SubBlocks[0].Type)
	}
	if cb.SubBlocks[1].Type != "image" {
		t.Errorf("expected second sub-block type=image, got %q", cb.SubBlocks[1].Type)
	}
	if cb.SubBlocks[1].MediaType != "image/png" {
		t.Errorf("expected image/png, got %q", cb.SubBlocks[1].MediaType)
	}
}

func TestAgenticLoop_ImageOnlyToolResult(t *testing.T) {
	dir := t.TempDir()
	node := makeAgentNode(t, []string{"render@v1"}, 5, "")

	reg := NewRegistry()
	imgData := []byte{0x47, 0x49, 0x46} // GIF magic
	sig := bundle.ToolSignature{Description: "renders image", InputSchema: json.RawMessage(`{"type":"object"}`)}
	_ = reg.Register("render@v1", sig, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{
			"frame": ToolImageOutput{Data: imgData, MediaType: "image/gif"},
		}, nil
	}))

	execCtx := NewExecutionContext(map[string]any{})
	provider := &scriptedProvider{t: t, responses: []CompletionResponse{
		{ToolCalls: []ToolCall{{ID: "tc1", Name: "render__v1", Input: json.RawMessage(`{}`)}}, StopReason: "tool_use"},
		{Content: "rendered", StopReason: "end_turn"},
	}}

	_, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, reg, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Image-only output: no text sub-block, one image sub-block.
	secondCallMsgs := provider.calls[1].Messages
	toolResultMsg := secondCallMsgs[len(secondCallMsgs)-1]
	cb := toolResultMsg.Blocks[0]
	if len(cb.SubBlocks) != 1 {
		t.Fatalf("expected 1 sub-block (image only), got %d", len(cb.SubBlocks))
	}
	if cb.SubBlocks[0].Type != "image" {
		t.Errorf("expected image sub-block, got %q", cb.SubBlocks[0].Type)
	}
}

func TestParseToolConfig_MaxIterZero(t *testing.T) {
	config := map[string]json.RawMessage{
		"tools":              json.RawMessage(`["anthropic:web_search"]`),
		"max_tool_iterations": json.RawMessage(`0`),
	}
	_, _, _, _, err := parseToolConfig(config, nil)
	if err == nil {
		t.Fatal("expected error for max_tool_iterations=0")
	}
	if !strings.Contains(err.Error(), "positive integer") {
		t.Errorf("expected 'positive integer' in error, got %q", err.Error())
	}
}

func TestSanitizeToolName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"search@v1", "search__v1"},
		{"web-search@v2", "web-search__v2"},
		{"my.tool@1.0", "my_tool__1_0"},
		{"simple", "simple"},
	}
	for _, c := range cases {
		got := sanitizeToolName(c.in)
		if got != c.want {
			t.Errorf("sanitizeToolName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
