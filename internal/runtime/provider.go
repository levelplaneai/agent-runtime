package runtime

import "context"

// FileValue holds binary file content supplied as a flow input.
// CLI accepts --input key=@/path/to/file to populate these.
type FileValue struct {
	Name      string
	Data      []byte
	MediaType string // MIME type, e.g. "application/pdf", "image/png"
}

// ContentBlock is one piece of multimodal content inside a Message.
type ContentBlock struct {
	Type      string // "text" | "image" | "document"
	Text      string // when Type == "text"
	Data      []byte // raw bytes when Type == "image" or "document"
	MediaType string // MIME type of Data
}

// Message is a single turn in a conversation.
// When Blocks is non-nil it takes precedence over Content.
type Message struct {
	Role    string         // "user" | "assistant"
	Content string         // plain text shorthand; used when Blocks is nil
	Blocks  []ContentBlock // multimodal content; used when non-nil
}

// CompletionRequest is the provider-agnostic input for a chat completion.
type CompletionRequest struct {
	Model        string         // "provider/model" (e.g. "openai/gpt-4o") or bare model name
	MaxTokens    int
	System       string         // empty = no system prompt
	Messages     []Message
	Temperature  *float64       // nil = use provider default
	OutputSchema map[string]any // nil = plain text; non-nil = request structured JSON
}

// CompletionResponse is the provider-agnostic output of a chat completion.
type CompletionResponse struct {
	Content      string // raw text, or a JSON string when OutputSchema was set
	InputTokens  int64  // 0 when the provider does not report usage
	OutputTokens int64  // 0 when the provider does not report usage
}

// LLMProvider executes chat completions against a model API.
type LLMProvider interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}
