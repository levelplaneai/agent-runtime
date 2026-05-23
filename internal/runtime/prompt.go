package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/aditya-vinodh/agent-runtime/internal/bundle"
)

// ExecutePrompt executes a prompt node:
//  1. Resolves declared inputs via their "from" bindings.
//  2. Assembles system + user messages (inline config strings or .prompt files).
//  3. Renders {{ name }} templates using the resolved inputs.
//  4. Calls the LLM via the provider (supports Anthropic, OpenAI, Gemini, etc.).
//  5. If output_schema is declared, structured JSON output is requested.
//  6. Returns the parsed output map.
//
// nodeDir is the absolute path to the node's version directory
// (e.g. "<bundle>/nodes/extract_items/v3/"). It is used to load
// system.prompt / user.prompt when they are not inlined in config.
func ExecutePrompt(
	ctx context.Context,
	node bundle.Node,
	nodeDir string,
	execCtx *ExecutionContext,
	provider LLMProvider,
) (map[string]any, error) {
	resolved, err := resolveNodeInputs(node, execCtx)
	if err != nil {
		return nil, err
	}

	model, err := configString(node.Config, "model")
	if err != nil {
		return nil, fmt.Errorf("prompt node: %w", err)
	}

	req, err := buildCompletionRequest(node, nodeDir, resolved, model)
	if err != nil {
		return nil, fmt.Errorf("prompt node: %w", err)
	}

	t := tracerFrom(ctx)
	t.Emit(TraceEvent{Event: "llm_request", Node: execCtx.CurrentNode(), Model: req.Model})
	reqStart := time.Now()

	resp, err := provider.Complete(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("prompt node: %w", err)
	}

	t.Emit(TraceEvent{
		Event:        "llm_response",
		Node:         execCtx.CurrentNode(),
		Model:        req.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		DurationMS:   time.Since(reqStart).Milliseconds(),
	})

	return extractOutput(resp)
}

// buildCompletionRequest assembles the provider-agnostic CompletionRequest for a prompt node.
func buildCompletionRequest(
	node bundle.Node,
	nodeDir string,
	inputs map[string]any,
	model string,
) (CompletionRequest, error) {
	req := CompletionRequest{
		Model:     model,
		MaxTokens: 16000,
	}

	// --- System prompt ---
	systemText, err := resolveText(node.Config, "system", filepath.Join(nodeDir, "system.prompt"))
	if err != nil {
		return req, fmt.Errorf("system: %w", err)
	}
	if systemText != "" {
		rendered, err := Render(systemText, inputs)
		if err != nil {
			return req, fmt.Errorf("rendering system: %w", err)
		}
		req.System = rendered
	}

	// --- Messages ---
	messages, err := buildMessages(node.Config, nodeDir, inputs)
	if err != nil {
		return req, err
	}
	req.Messages = messages

	// --- Structured output schema ---
	if len(node.OutputSchema) > 0 {
		schema, err := rawSchemaToMap(node.OutputSchema)
		if err != nil {
			return req, fmt.Errorf("converting output_schema: %w", err)
		}
		req.OutputSchema = schema
	}

	// --- Optional temperature ---
	if raw, ok := node.Config["temperature"]; ok {
		var temp float64
		if err := json.Unmarshal(raw, &temp); err == nil {
			req.Temperature = &temp
		}
	}

	return req, nil
}

// buildMessages constructs the messages slice.
// If config.messages is present (multi-turn), it is used directly after template rendering.
// Otherwise a single user turn is built from config.user or user.prompt.
// Any FileValue inputs are appended as content blocks after the text block.
func buildMessages(
	config map[string]json.RawMessage,
	nodeDir string,
	inputs map[string]any,
) ([]Message, error) {
	if raw, ok := config["messages"]; ok {
		return buildMultiTurnMessages(raw, inputs)
	}

	userText, err := resolveText(config, "user", filepath.Join(nodeDir, "user.prompt"))
	if err != nil {
		return nil, fmt.Errorf("user: %w", err)
	}
	if userText == "" {
		return nil, fmt.Errorf("prompt node needs config.user or user.prompt")
	}
	rendered, err := Render(userText, inputs)
	if err != nil {
		return nil, fmt.Errorf("rendering user: %w", err)
	}

	fileBlocks := collectFileBlocks(inputs)
	if len(fileBlocks) == 0 {
		return []Message{{Role: "user", Content: rendered}}, nil
	}

	blocks := make([]ContentBlock, 0, 1+len(fileBlocks))
	blocks = append(blocks, ContentBlock{Type: "text", Text: rendered})
	blocks = append(blocks, fileBlocks...)
	return []Message{{Role: "user", Blocks: blocks}}, nil
}

// collectFileBlocks extracts FileValue inputs and returns them as ContentBlocks.
func collectFileBlocks(inputs map[string]any) []ContentBlock {
	var blocks []ContentBlock
	for _, v := range inputs {
		fv, ok := v.(FileValue)
		if !ok {
			continue
		}
		kind := "document"
		if strings.HasPrefix(fv.MediaType, "image/") {
			kind = "image"
		}
		blocks = append(blocks, ContentBlock{
			Type:      kind,
			Data:      fv.Data,
			MediaType: fv.MediaType,
		})
	}
	return blocks
}

// buildMultiTurnMessages parses a config.messages JSON array and renders templates.
func buildMultiTurnMessages(raw json.RawMessage, inputs map[string]any) ([]Message, error) {
	var turns []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(raw, &turns); err != nil {
		return nil, fmt.Errorf("config.messages must be an array of {role, content}: %w", err)
	}

	out := make([]Message, 0, len(turns))
	for i, t := range turns {
		rendered, err := Render(t.Content, inputs)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		role := strings.ToLower(t.Role)
		if role != "user" && role != "assistant" {
			return nil, fmt.Errorf("messages[%d]: unknown role %q", i, t.Role)
		}
		out = append(out, Message{Role: role, Content: rendered})
	}
	return out, nil
}

// resolveText returns the text for a prompt component. It checks the config key
// first (inline string) and falls back to reading the given file. Returns "" if
// neither is present.
func resolveText(config map[string]json.RawMessage, key string, filePath string) (string, error) {
	if raw, ok := config[key]; ok {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", fmt.Errorf("config.%s must be a string: %w", key, err)
		}
		return s, nil
	}
	data, err := os.ReadFile(filePath)
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", filepath.Base(filePath), err)
	}
	return string(data), nil
}

// rawSchemaToMap converts the node's OutputSchema (map[string]json.RawMessage)
// to a map[string]any suitable for passing to providers.
func rawSchemaToMap(schema map[string]json.RawMessage) (map[string]any, error) {
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// extractOutput parses the provider response content and returns a map.
// With a structured schema the content is expected to be valid JSON.
// When no schema was declared the raw text is returned under the "text" key.
func extractOutput(resp CompletionResponse) (map[string]any, error) {
	var out map[string]any
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil {
		return map[string]any{"text": resp.Content}, nil
	}
	return out, nil
}
