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

	out, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, nil, nil, nil)
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

	out, err := ExecutePrompt(context.Background(), node, dir, execCtx, provider, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["text"] != "hello world" {
		t.Errorf("unexpected output: %v", out)
	}
}

func TestBuildToolResultContent_TextOnly(t *testing.T) {
	output := map[string]any{"value": "42", "unit": "celsius"}
	text, subBlocks := buildToolResultContent(output)
	if len(subBlocks) != 0 {
		t.Errorf("expected no sub-blocks for text-only output, got %d", len(subBlocks))
	}
	if text == "" {
		t.Error("expected non-empty text for text-only output")
	}
}

func TestBuildToolResultContent_ImageOnly(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4e, 0x47} // PNG magic bytes
	output := map[string]any{
		"screenshot": ToolImageOutput{Data: imgData, MediaType: "image/png"},
	}
	text, subBlocks := buildToolResultContent(output)
	if text != "" {
		t.Errorf("expected empty text when only image in output, got %q", text)
	}
	if len(subBlocks) != 1 {
		t.Fatalf("expected 1 sub-block, got %d", len(subBlocks))
	}
	if subBlocks[0].Type != "image" {
		t.Errorf("expected image sub-block, got %q", subBlocks[0].Type)
	}
	if string(subBlocks[0].Data) != string(imgData) {
		t.Error("image data mismatch")
	}
	if subBlocks[0].MediaType != "image/png" {
		t.Errorf("expected image/png, got %q", subBlocks[0].MediaType)
	}
}

func TestBuildToolResultContent_MixedTextAndImage(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4e, 0x47}
	output := map[string]any{
		"status":     "ok",
		"screenshot": ToolImageOutput{Data: imgData, MediaType: "image/png"},
	}
	text, subBlocks := buildToolResultContent(output)
	if text != "" {
		t.Errorf("expected empty text when sub-blocks are present, got %q", text)
	}
	if len(subBlocks) != 2 {
		t.Fatalf("expected 2 sub-blocks (text + image), got %d", len(subBlocks))
	}
	// First sub-block must be text with remaining JSON fields.
	if subBlocks[0].Type != "text" {
		t.Errorf("expected first sub-block type=text, got %q", subBlocks[0].Type)
	}
	if subBlocks[0].Text == "" {
		t.Error("expected non-empty text in first sub-block")
	}
	// Second sub-block must be image.
	if subBlocks[1].Type != "image" {
		t.Errorf("expected second sub-block type=image, got %q", subBlocks[1].Type)
	}
}

func TestSanitizeOutputForTrace_ReplacesImages(t *testing.T) {
	imgData := make([]byte, 1024)
	output := map[string]any{
		"label": "chart",
		"image": ToolImageOutput{Data: imgData, MediaType: "image/jpeg"},
	}
	sanitized := sanitizeOutputForTrace(output)
	if sanitized["label"] != "chart" {
		t.Errorf("non-image field altered: %v", sanitized["label"])
	}
	summary, ok := sanitized["image"].(map[string]any)
	if !ok {
		t.Fatalf("expected map summary for image field, got %T", sanitized["image"])
	}
	if summary["type"] != "image" {
		t.Errorf("expected type=image in summary, got %v", summary["type"])
	}
	if summary["size"] != 1024 {
		t.Errorf("expected size=1024, got %v", summary["size"])
	}
	if summary["mediaType"] != "image/jpeg" {
		t.Errorf("expected mediaType=image/jpeg, got %v", summary["mediaType"])
	}
}

func TestSanitizeOutputForTrace_PassthroughNonImage(t *testing.T) {
	output := map[string]any{"temperature": 42.0, "unit": "celsius"}
	sanitized := sanitizeOutputForTrace(output)
	if sanitized["temperature"] != 42.0 || sanitized["unit"] != "celsius" {
		t.Errorf("non-image fields should pass through unchanged: %v", sanitized)
	}
}

// --- Gap 1: []FileValue in collectFileBlocks ---

func TestCollectFileBlocks_FileValueSlice(t *testing.T) {
	pngMagic := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	inputs := map[string]any{
		"crops": []FileValue{
			{Name: "front.png", Data: pngMagic, MediaType: "image/png"},
			{Name: "top.png", Data: pngMagic, MediaType: "image/png"},
		},
	}
	blocks := collectFileBlocks(inputs)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 image blocks from []FileValue, got %d", len(blocks))
	}
	for i, b := range blocks {
		if b.Type != "image" {
			t.Errorf("block[%d]: expected type=image, got %q", i, b.Type)
		}
		if b.MediaType != "image/png" {
			t.Errorf("block[%d]: expected image/png, got %q", i, b.MediaType)
		}
	}
}

func TestCollectFileBlocks_SingleFileValue(t *testing.T) {
	inputs := map[string]any{
		"doc": FileValue{Name: "spec.pdf", Data: []byte("%PDF"), MediaType: "application/pdf"},
	}
	blocks := collectFileBlocks(inputs)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0].Type != "document" {
		t.Errorf("expected type=document for PDF, got %q", blocks[0].Type)
	}
}

// --- Gap 2: content-array messages ---

func TestBuildMultiTurnMessages_StringContent(t *testing.T) {
	turns := []map[string]any{
		{"role": "user", "content": "Hello {{ name }}"},
	}
	raw, _ := json.Marshal(turns)
	msgs, err := buildMultiTurnMessages(raw, map[string]any{"name": "World"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Content != "Hello World" {
		t.Errorf("unexpected messages: %v", msgs)
	}
}

func TestBuildMultiTurnMessages_ContentArray_TextAndImage(t *testing.T) {
	pngMagic := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	inputs := map[string]any{
		"drawing": FileValue{Name: "draw.png", Data: pngMagic, MediaType: "image/png"},
	}
	turns := []map[string]any{
		{"role": "user", "content": []map[string]any{
			{"type": "text", "value": "Original Diagram:"},
			{"type": "image", "input": "drawing"},
		}},
	}
	raw, _ := json.Marshal(turns)
	msgs, err := buildMultiTurnMessages(raw, inputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Blocks == nil {
		t.Fatal("expected Blocks to be set for content-array message")
	}
	if len(msgs[0].Blocks) != 2 {
		t.Fatalf("expected 2 blocks (text + image), got %d", len(msgs[0].Blocks))
	}
	if msgs[0].Blocks[0].Type != "text" || msgs[0].Blocks[0].Text != "Original Diagram:" {
		t.Errorf("first block wrong: %+v", msgs[0].Blocks[0])
	}
	if msgs[0].Blocks[1].Type != "image" {
		t.Errorf("second block should be image, got %q", msgs[0].Blocks[1].Type)
	}
}

func TestBuildMultiTurnMessages_ContentArray_ImageSequence_FileValues(t *testing.T) {
	pngMagic := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	inputs := map[string]any{
		"crops": []FileValue{
			{Name: "front.png", Data: pngMagic, MediaType: "image/png"},
			{Name: "top.png", Data: pngMagic, MediaType: "image/png"},
		},
	}
	turns := []map[string]any{
		{"role": "user", "content": []map[string]any{
			{"type": "image_sequence", "input": "crops", "label": "View: {{ name }}"},
		}},
	}
	raw, _ := json.Marshal(turns)
	msgs, err := buildMultiTurnMessages(raw, inputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expect: [text "View: front.png", image, text "View: top.png", image]
	if len(msgs[0].Blocks) != 4 {
		t.Fatalf("expected 4 blocks (label+image per item), got %d: %+v", len(msgs[0].Blocks), msgs[0].Blocks)
	}
	if msgs[0].Blocks[0].Type != "text" {
		t.Errorf("block[0] should be text label, got %q", msgs[0].Blocks[0].Type)
	}
	if msgs[0].Blocks[1].Type != "image" {
		t.Errorf("block[1] should be image, got %q", msgs[0].Blocks[1].Type)
	}
}

func TestBuildMultiTurnMessages_ContentArray_ImageSequence_Objects(t *testing.T) {
	// Write a temp PNG so loadFileValue can read it.
	dir := t.TempDir()
	pngMagic := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	front := filepath.Join(dir, "front.png")
	top := filepath.Join(dir, "top.png")
	if err := os.WriteFile(front, pngMagic, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(top, pngMagic, 0o644); err != nil {
		t.Fatal(err)
	}

	inputs := map[string]any{
		"views": []any{
			map[string]any{"name": "Front View", "path": front},
			map[string]any{"name": "Top View", "path": top},
		},
	}
	turns := []map[string]any{
		{"role": "user", "content": []map[string]any{
			{"type": "image_sequence", "input": "views", "label": "View Name: {{ name }}", "image_key": "path"},
		}},
	}
	raw, _ := json.Marshal(turns)
	msgs, err := buildMultiTurnMessages(raw, inputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expect: [text "View Name: Front View", image, text "View Name: Top View", image]
	if len(msgs[0].Blocks) != 4 {
		t.Fatalf("expected 4 blocks, got %d", len(msgs[0].Blocks))
	}
	if msgs[0].Blocks[0].Text != "View Name: Front View" {
		t.Errorf("first label wrong: %q", msgs[0].Blocks[0].Text)
	}
	if msgs[0].Blocks[2].Text != "View Name: Top View" {
		t.Errorf("third label wrong: %q", msgs[0].Blocks[2].Text)
	}
}

func TestBuildMultiTurnMessages_MixedStringAndArray(t *testing.T) {
	pngMagic := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	inputs := map[string]any{
		"img": FileValue{Name: "x.png", Data: pngMagic, MediaType: "image/png"},
	}
	turns := []map[string]any{
		{"role": "user", "content": "plain text"},
		{"role": "assistant", "content": "response"},
		{"role": "user", "content": []map[string]any{
			{"type": "image", "input": "img"},
		}},
	}
	raw, _ := json.Marshal(turns)
	msgs, err := buildMultiTurnMessages(raw, inputs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}
	if msgs[0].Content != "plain text" {
		t.Errorf("first message content wrong: %q", msgs[0].Content)
	}
	if msgs[2].Blocks == nil || msgs[2].Blocks[0].Type != "image" {
		t.Errorf("third message should have image block")
	}
}

// --- Gap 3: max_tokens config ---

func TestBuildCompletionRequest_MaxTokens_Default(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user.prompt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	node := makeNode(t, `{"type":"prompt","inputs":{},"config":{"model":"anthropic/claude-haiku-4-5"}}`)
	req, err := buildCompletionRequest(node, dir, map[string]any{}, "anthropic/claude-haiku-4-5")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.MaxTokens != 16000 {
		t.Errorf("expected default max_tokens=16000, got %d", req.MaxTokens)
	}
}

func TestBuildCompletionRequest_MaxTokens_Custom(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "user.prompt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	node := makeNode(t, `{
		"type": "prompt", "inputs": {},
		"config": {"model": "gemini/gemini-3.1-pro-preview", "max_tokens": 65536}
	}`)
	req, err := buildCompletionRequest(node, dir, map[string]any{}, "gemini/gemini-3.1-pro-preview")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if req.MaxTokens != 65536 {
		t.Errorf("expected max_tokens=65536, got %d", req.MaxTokens)
	}
}

func TestCollectFileBlocks_ToolImageOutput(t *testing.T) {
	imgData := []byte{0xff, 0xd8, 0xff} // JPEG magic
	inputs := map[string]any{
		"photo": ToolImageOutput{Data: imgData, MediaType: "image/jpeg"},
		"name":  "test",
	}
	blocks := collectFileBlocks(inputs)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 image block, got %d", len(blocks))
	}
	if blocks[0].Type != "image" {
		t.Errorf("expected type=image, got %q", blocks[0].Type)
	}
	if blocks[0].MediaType != "image/jpeg" {
		t.Errorf("expected image/jpeg, got %q", blocks[0].MediaType)
	}
}
