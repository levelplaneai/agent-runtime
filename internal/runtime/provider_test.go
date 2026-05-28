package runtime

import (
	"encoding/json"
	"testing"
)

// TestAnthropicBlocks_ToolResultTextOnly verifies the existing text-only path is unchanged.
func TestAnthropicBlocks_ToolResultTextOnly(t *testing.T) {
	m := Message{
		Role: "user",
		Blocks: []ContentBlock{
			{Type: "tool_result", ToolUseID: "tc1", ToolName: "tool", Text: `{"value":42}`},
		},
	}
	blocks, err := anthropicBlocks(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
}

// TestAnthropicBlocks_ToolResultWithImage verifies that SubBlocks (text+image) produce
// a ToolResultBlockParam with two content items.
func TestAnthropicBlocks_ToolResultWithImage(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4e, 0x47}
	m := Message{
		Role: "user",
		Blocks: []ContentBlock{
			{
				Type:      "tool_result",
				ToolUseID: "tc1",
				ToolName:  "screenshot__v1",
				SubBlocks: []ContentBlock{
					{Type: "text", Text: `{"label":"desktop"}`},
					{Type: "image", Data: imgData, MediaType: "image/png"},
				},
			},
		},
	}
	blocks, err := anthropicBlocks(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 top-level block, got %d", len(blocks))
	}
	// The block should be a ToolResult with two content items.
	toolResult := blocks[0].OfToolResult
	if toolResult == nil {
		t.Fatal("expected OfToolResult to be set")
	}
	if len(toolResult.Content) != 2 {
		t.Fatalf("expected 2 content items in tool result, got %d", len(toolResult.Content))
	}
	if toolResult.Content[0].OfText == nil {
		t.Error("expected first content item to be text")
	}
	if toolResult.Content[1].OfImage == nil {
		t.Error("expected second content item to be image")
	}
}

// TestAnthropicBlocks_ToolResultImageOnly verifies that an image-only SubBlocks list
// produces a single image content item (no text item).
func TestAnthropicBlocks_ToolResultImageOnly(t *testing.T) {
	imgData := []byte{0x47, 0x49, 0x46}
	m := Message{
		Role: "user",
		Blocks: []ContentBlock{
			{
				Type:      "tool_result",
				ToolUseID: "tc1",
				ToolName:  "render__v1",
				SubBlocks: []ContentBlock{
					{Type: "image", Data: imgData, MediaType: "image/gif"},
				},
			},
		},
	}
	blocks, err := anthropicBlocks(m)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	toolResult := blocks[0].OfToolResult
	if toolResult == nil {
		t.Fatal("expected OfToolResult to be set")
	}
	if len(toolResult.Content) != 1 {
		t.Fatalf("expected 1 content item, got %d", len(toolResult.Content))
	}
	if toolResult.Content[0].OfImage == nil {
		t.Error("expected image content item")
	}
}

// TestGeminiContents_ToolResultTextOnly verifies the existing text-only path is unchanged.
func TestGeminiContents_ToolResultTextOnly(t *testing.T) {
	msgs := []Message{
		{
			Role: "user",
			Blocks: []ContentBlock{
				{Type: "tool_result", ToolName: "tool", Text: `{"value":42}`},
			},
		},
	}
	contents, err := buildGeminiContents(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 1 || len(contents[0].Parts) != 1 {
		t.Fatalf("expected 1 content with 1 part, got %+v", contents)
	}
	if contents[0].Parts[0].FunctionResponse == nil {
		t.Error("expected FunctionResponse part")
	}
	if contents[0].Parts[0].FunctionResponse.Parts != nil {
		t.Error("expected no FunctionResponse.Parts for text-only result")
	}
}

// TestGeminiContents_ToolResultWithImage verifies that image sub-blocks produce
// FunctionResponse.Parts with inline data.
func TestGeminiContents_ToolResultWithImage(t *testing.T) {
	imgData := []byte{0x89, 0x50, 0x4e, 0x47}
	msgs := []Message{
		{
			Role: "user",
			Blocks: []ContentBlock{
				{
					Type:     "tool_result",
					ToolName: "screenshot__v1",
					Text:     `{"label":"desktop"}`,
					SubBlocks: []ContentBlock{
						{Type: "text", Text: `{"label":"desktop"}`},
						{Type: "image", Data: imgData, MediaType: "image/png"},
					},
				},
			},
		},
	}
	contents, err := buildGeminiContents(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(contents) != 1 || len(contents[0].Parts) != 1 {
		t.Fatalf("expected 1 content with 1 part, got %+v", contents)
	}
	fr := contents[0].Parts[0].FunctionResponse
	if fr == nil {
		t.Fatal("expected FunctionResponse part")
	}
	if len(fr.Parts) != 1 {
		t.Fatalf("expected 1 FunctionResponse.Part (the image), got %d", len(fr.Parts))
	}
	if fr.Parts[0].InlineData == nil {
		t.Error("expected InlineData in FunctionResponse part")
	}
	if fr.Parts[0].InlineData.MIMEType != "image/png" {
		t.Errorf("expected image/png, got %q", fr.Parts[0].InlineData.MIMEType)
	}
}

// TestGeminiContents_ToolResultIsError verifies error path still works with SubBlocks.
func TestGeminiContents_ToolResultIsError(t *testing.T) {
	msgs := []Message{
		{
			Role: "user",
			Blocks: []ContentBlock{
				{Type: "tool_result", ToolName: "fail__v1", Text: "tool exploded", IsError: true},
			},
		},
	}
	contents, err := buildGeminiContents(msgs)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fr := contents[0].Parts[0].FunctionResponse
	if fr == nil {
		t.Fatal("expected FunctionResponse")
	}
	errVal, _ := fr.Response["error"].(string)
	if errVal != "tool exploded" {
		t.Errorf("expected error in response map, got %v", fr.Response)
	}
}

// makeNode is shared across test files in this package; defined in toolcall_test.go or similar.
// We need json for these tests — it's already imported.
var _ = json.Marshal // ensure json import is used
