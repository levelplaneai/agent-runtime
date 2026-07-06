package runtime

import (
	"encoding/json"
	"testing"
)

func TestOpenaiEncodeMessage_AssistantTextContentArray(t *testing.T) {
	msg := Message{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: "text", Text: "Hello, "},
			{Type: "text", Text: "world."},
		},
	}
	out, err := openaiEncodeMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 param, got %d", len(out))
	}
	b, err := json.Marshal(out[0])
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}
	var decoded struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if decoded.Content != "Hello, world." {
		t.Errorf("expected assistant text blocks to be concatenated into content, got %q (raw: %s)", decoded.Content, b)
	}
}

func TestOpenaiEncodeMessage_AssistantImageBlockRejected(t *testing.T) {
	msg := Message{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: "image", Data: []byte{0x89, 0x50, 0x4e, 0x47}, MediaType: "image/png"},
		},
	}
	_, err := openaiEncodeMessage(msg)
	if err == nil {
		t.Fatal("expected error for unsupported assistant image block, got nil")
	}
}

func TestOpenaiEncodeMessage_AssistantToolUseStillWorks(t *testing.T) {
	msg := Message{
		Role: "assistant",
		Blocks: []ContentBlock{
			{Type: "text", Text: "calling a tool"},
			{Type: "tool_use", ToolUseID: "call_1", ToolName: "search", ToolInput: json.RawMessage(`{"q":"x"}`)},
		},
	}
	out, err := openaiEncodeMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 param, got %d", len(out))
	}
}

func TestOpenaiEncodeMessage_AssistantPlainString(t *testing.T) {
	msg := Message{Role: "assistant", Content: "hi there"}
	out, err := openaiEncodeMessage(msg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 param, got %d", len(out))
	}
}
