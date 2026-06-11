package runtime

import (
	"context"
	"encoding/json"
)

// FileValue holds binary file content supplied as a flow input.
// CLI accepts --input key=@/path/to/file to populate these.
type FileValue struct {
	Name      string
	Data      []byte
	MediaType string // MIME type, e.g. "application/pdf", "image/png"
}

// ToolImageOutput can be included as a value in a tool's output map to return
// image data to the LLM alongside JSON-serializable fields. Non-image fields
// in the same map are serialized as a text block; image fields become image blocks.
type ToolImageOutput struct {
	Data      []byte
	MediaType string // "image/png", "image/jpeg", "image/gif", "image/webp"
}

// ContentBlock is one piece of multimodal content inside a Message.
// When Blocks is non-nil in a Message, it takes precedence over Content.
type ContentBlock struct {
	Type      string // "text" | "image" | "document" | "tool_use" | "tool_result"
	Text      string // when Type == "text" or tool_result output (JSON string)
	Data      []byte // raw bytes when Type == "image" or "document"
	MediaType string // MIME type of Data

	// Tool-use fields; only populated when Type == "tool_use" or "tool_result".
	ToolUseID string          // LLM-assigned call ID
	ToolName  string          // tool name called / being responded to
	ToolInput json.RawMessage // raw JSON args from LLM (Type == "tool_use")
	IsError   bool            // marks a tool_result as an error

	// SubBlocks holds inner content for a "tool_result" with multimodal output
	// (e.g. text + images). When non-nil, providers use these instead of Text.
	SubBlocks []ContentBlock
}

// Message is a single turn in a conversation.
// When Blocks is non-nil it takes precedence over Content.
type Message struct {
	Role    string         // "user" | "assistant"
	Content string         // plain text shorthand; used when Blocks is nil
	Blocks  []ContentBlock // multimodal or tool-use content; used when non-nil
}

// BuiltinTool declares a provider-hosted tool that the provider executes on its own
// servers. No local implementation is required. Name uses "provider:toolname" format,
// e.g. "anthropic:web_search" or "gemini:code_execution".
type BuiltinTool struct {
	Name string
}

// ToolDefinition describes a tool available to the LLM during an agentic prompt loop.
type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any // JSON Schema for tool arguments
}

// ToolCall is a tool invocation requested by the LLM.
type ToolCall struct {
	ID    string          // provider-assigned call ID
	Name  string          // sanitized tool name (matches ToolDefinition.Name)
	Input json.RawMessage // raw JSON arguments
}

// CompletionRequest is the provider-agnostic input for a chat completion.
type CompletionRequest struct {
	Model        string         // "provider/model" (e.g. "openai/gpt-4o") or bare model name
	MaxTokens    int
	System       string         // empty = no system prompt
	Messages     []Message
	Temperature     *float64       // nil = use provider default
	ThinkingBudget  *int           // nil=not set; 0=disable thinking; >0=token budget (Gemini, Anthropic)
	ReasoningEffort *string        // nil=not set; "low"/"medium"/"high" (OpenAI o-series, Gemini)
	OutputSchema    map[string]any // nil = plain text; non-nil = request structured JSON
	Tools        []ToolDefinition // nil = single-shot completion; non-nil = agentic tool-use
	BuiltinTools []BuiltinTool    // provider-hosted tools (e.g. "anthropic:web_search")
}

// CompletionResponse is the provider-agnostic output of a chat completion.
type CompletionResponse struct {
	Content         string     // final text answer (or JSON string when OutputSchema was set)
	ToolCalls       []ToolCall // non-empty when LLM requested tool calls (Content may also be set)
	StopReason      string     // "end_turn" | "tool_use" | "max_tokens"
	InputTokens     int64      // 0 when the provider does not report usage
	OutputTokens    int64      // 0 when the provider does not report usage
	BuiltinToolsUsed []string  // names of server-side tools that fired (e.g. "anthropic:web_search")
}

// appendUnique appends s to slice only if not already present.
func appendUnique(slice []string, s string) []string {
	for _, v := range slice {
		if v == s {
			return slice
		}
	}
	return append(slice, s)
}

// LLMProvider executes chat completions against a model API.
type LLMProvider interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}
