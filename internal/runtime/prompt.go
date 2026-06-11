package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// ExecutePrompt executes a prompt node:
//  1. Resolves declared inputs via their "from" bindings.
//  2. Assembles system + user messages (inline config strings or .prompt files).
//  3. Renders {{ name }} templates using the resolved inputs.
//  4. If config.tools is set, runs an agentic tool-use loop until the LLM gives
//     a final text answer or max_tool_iterations is reached.
//  5. Otherwise calls the LLM once (single-shot).
//  6. If output_schema is declared, structured JSON output is requested.
//  7. Returns the parsed output map.
//
// nodeDir is the absolute path to the node's version directory
// (e.g. "<bundle>/nodes/extract_items/v3/"). It is used to load
// system.prompt / user.prompt when they are not inlined in config.
// reg may be nil when config.tools is absent.
func ExecutePrompt(
	ctx context.Context,
	node bundle.Node,
	nodeDir string,
	execCtx *ExecutionContext,
	provider LLMProvider,
	reg *Registry,
	seedLoopState *NodeSnapshot,
	onLoopCheckpoint func(NodeSnapshot) error,
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

	toolDefs, builtins, refMap, maxIter, err := parseToolConfig(node.Config, reg)
	if err != nil {
		return nil, fmt.Errorf("prompt node: %w", err)
	}

	// Cross-provider validation: built-in tool prefix must match the model's provider.
	modelPrefix := strings.SplitN(req.Model, "/", 2)[0]
	for _, bt := range builtins {
		toolPrefix := strings.SplitN(bt.Name, ":", 2)[0]
		if toolPrefix != modelPrefix {
			return nil, fmt.Errorf("prompt node: %s is a %s built-in tool but model is %s", bt.Name, toolPrefix, req.Model)
		}
	}

	req.BuiltinTools = builtins

	t := tracerFrom(ctx)
	nodeName := execCtx.CurrentNode()

	if len(toolDefs) == 0 {
		// --- single-shot path (existing behaviour) ---
		t.Emit(TraceEvent{Event: "llm_request", Node: nodeName, Model: req.Model, Inputs: requestToTrace(req)})
		reqStart := time.Now()

		resp, err := provider.Complete(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("prompt node: %w", err)
		}

		t.Emit(TraceEvent{
			Event:        "llm_response",
			Node:         nodeName,
			Model:        req.Model,
			InputTokens:  resp.InputTokens,
			OutputTokens: resp.OutputTokens,
			DurationMS:   time.Since(reqStart).Milliseconds(),
			Output:       map[string]any{"text": resp.Content},
		})
		for _, name := range resp.BuiltinToolsUsed {
			t.Emit(TraceEvent{Event: "builtin_tool_used", Node: nodeName, Tool: name})
		}
		return extractOutput(resp)
	}

	// --- agentic tool-use loop ---
	req.Tools = toolDefs
	messages := req.Messages
	startIter := 0
	if seedLoopState != nil {
		messages = seedLoopState.Messages
		startIter = seedLoopState.Iteration
	}

	for iter := startIter; iter < maxIter; iter++ {
		t.Emit(TraceEvent{Event: "agent_iteration", Node: nodeName, Attempt: iter + 1})
		req.Messages = messages

		t.Emit(TraceEvent{Event: "llm_request", Node: nodeName, Model: req.Model, Attempt: iter + 1, Inputs: requestToTrace(req)})
		iterStart := time.Now()

		resp, err := provider.Complete(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("prompt node agentic loop: %w", err)
		}

		t.Emit(TraceEvent{
			Event:        "llm_response",
			Node:         nodeName,
			Model:        req.Model,
			InputTokens:  resp.InputTokens,
			OutputTokens: resp.OutputTokens,
			DurationMS:   time.Since(iterStart).Milliseconds(),
			Attempt:      iter + 1,
			Output:       map[string]any{"text": resp.Content},
		})
		for _, name := range resp.BuiltinToolsUsed {
			t.Emit(TraceEvent{Event: "builtin_tool_used", Node: nodeName, Tool: name, Attempt: iter + 1})
		}

		if len(resp.ToolCalls) == 0 {
			return extractOutput(resp)
		}

		// Append assistant turn with tool_use blocks (and optional text prefix).
		messages = append(messages, buildAssistantTurnMessage(resp))

		// Execute each tool call and collect tool_result blocks.
		resultBlocks := make([]ContentBlock, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			ref, ok := refMap[tc.Name]
			if !ok {
				resultBlocks = append(resultBlocks, ContentBlock{
					Type:      "tool_result",
					ToolUseID: tc.ID,
					ToolName:  tc.Name,
					Text:      fmt.Sprintf("unknown tool %q", tc.Name),
					IsError:   true,
				})
				continue
			}
			tool, _, _ := reg.Lookup(ref)

			var args map[string]any
			_ = json.Unmarshal(tc.Input, &args)

			t.Emit(TraceEvent{
				Event:   "tool_start",
				Node:    nodeName,
				Tool:    ref,
				Args:    args,
				Attempt: iter + 1,
			})
			toolStart := time.Now()
			output, toolErr := tool.Call(ctx, args)
			toolDur := time.Since(toolStart).Milliseconds()

			if toolErr != nil {
				t.Emit(TraceEvent{
					Event:      "tool_error",
					Node:       nodeName,
					Tool:       ref,
					Error:      toolErr.Error(),
					DurationMS: toolDur,
					Attempt:    iter + 1,
				})
				resultBlocks = append(resultBlocks, ContentBlock{
					Type:      "tool_result",
					ToolUseID: tc.ID,
					ToolName:  tc.Name,
					Text:      toolErr.Error(),
					IsError:   true,
				})
			} else {
				t.Emit(TraceEvent{
					Event:      "tool_done",
					Node:       nodeName,
					Tool:       ref,
					Output:     sanitizeOutputForTrace(output),
					DurationMS: toolDur,
					Attempt:    iter + 1,
				})
				textPart, subBlocks := buildToolResultContent(output)
				cb := ContentBlock{Type: "tool_result", ToolUseID: tc.ID, ToolName: tc.Name}
				if len(subBlocks) == 0 {
					cb.Text = textPart
				} else {
					cb.SubBlocks = subBlocks
				}
				resultBlocks = append(resultBlocks, cb)
			}
		}

		// Append user turn carrying all tool_results.
		messages = append(messages, Message{Role: "user", Blocks: resultBlocks})

		if onLoopCheckpoint != nil {
			ns := NodeSnapshot{NodeName: nodeName, NodeType: "prompt", Messages: messages, Iteration: iter + 1}
			if err := onLoopCheckpoint(ns); err != nil {
				return nil, err
			}
		}
	}

	return nil, fmt.Errorf("prompt node: agentic loop reached max_tool_iterations (%d)", maxIter)
}

// buildAssistantTurnMessage builds the assistant Message to append after tool calls.
// It includes an optional text block (if the LLM returned preamble text) followed
// by one tool_use block per requested call.
func buildAssistantTurnMessage(resp CompletionResponse) Message {
	var blocks []ContentBlock
	if resp.Content != "" {
		blocks = append(blocks, ContentBlock{Type: "text", Text: resp.Content})
	}
	for _, tc := range resp.ToolCalls {
		blocks = append(blocks, ContentBlock{
			Type:      "tool_use",
			ToolUseID: tc.ID,
			ToolName:  tc.Name,
			ToolInput: tc.Input,
		})
	}
	return Message{Role: "assistant", Blocks: blocks}
}

// knownBuiltinTools lists provider-managed tool names accepted in config.tools.
// The key is the full "provider:name" string.
var knownBuiltinTools = map[string]bool{
	"anthropic:web_search":    true,
	"anthropic:code_execution": true,
	"anthropic:bash":           true,
	"anthropic:text_editor":    true,
	"gemini:code_execution":    true,
	"gemini:google_search":     true,
	"gemini:url_context":       true,
}

// parseToolConfig parses config.tools and config.max_tool_iterations.
// Returns nil defs/builtins/refMap when config.tools is absent (caller uses single-shot path).
// reg may be nil when no registry tools are listed in config.tools.
// Registry refs use "name@version" format; built-in tool refs use "provider:toolname" format.
func parseToolConfig(
	config map[string]json.RawMessage,
	reg *Registry,
) (defs []ToolDefinition, builtins []BuiltinTool, refMap map[string]string, maxIter int, err error) {
	raw, ok := config["tools"]
	if !ok {
		return nil, nil, nil, 0, nil
	}

	var refs []string
	if err := json.Unmarshal(raw, &refs); err != nil {
		return nil, nil, nil, 0, fmt.Errorf("config.tools must be an array of strings: %w", err)
	}

	maxIter = 10
	if rawMax, ok := config["max_tool_iterations"]; ok {
		var n int
		if err := json.Unmarshal(rawMax, &n); err != nil {
			return nil, nil, nil, 0, fmt.Errorf("config.max_tool_iterations must be an integer: %w", err)
		}
		if n <= 0 {
			return nil, nil, nil, 0, fmt.Errorf("config.max_tool_iterations must be a positive integer, got %d", n)
		}
		maxIter = n
	}

	defs = make([]ToolDefinition, 0, len(refs))
	refMap = make(map[string]string, len(refs))

	for _, ref := range refs {
		if strings.Contains(ref, ":") {
			// Provider-managed built-in tool.
			prefix := strings.SplitN(ref, ":", 2)[0]
			if prefix == "openai" {
				return nil, nil, nil, 0, fmt.Errorf("tool %q: OpenAI does not support provider-managed built-in tools in the Chat Completions API", ref)
			}
			if !knownBuiltinTools[ref] {
				return nil, nil, nil, 0, fmt.Errorf("tool %q: unknown built-in tool (known: anthropic:web_search, anthropic:code_execution, anthropic:bash, anthropic:text_editor, gemini:code_execution, gemini:google_search, gemini:url_context)", ref)
			}
			builtins = append(builtins, BuiltinTool{Name: ref})
			continue
		}

		// Registry ref ("name@version").
		if reg == nil {
			return nil, nil, nil, 0, fmt.Errorf("tool %q requires a registry but none was provided", ref)
		}
		_, sig, ok := reg.Lookup(ref)
		if !ok {
			return nil, nil, nil, 0, fmt.Errorf("tool %q not found in registry", ref)
		}

		sanitized := sanitizeToolName(ref)

		var inputSchema map[string]any
		if len(sig.InputSchema) > 0 {
			if err := json.Unmarshal(sig.InputSchema, &inputSchema); err != nil {
				return nil, nil, nil, 0, fmt.Errorf("tool %q: invalid input_schema: %w", ref, err)
			}
		}

		defs = append(defs, ToolDefinition{
			Name:        sanitized,
			Description: sig.Description,
			InputSchema: inputSchema,
		})
		if existing, exists := refMap[sanitized]; exists {
			return nil, nil, nil, 0, fmt.Errorf("tool name collision: %q and %q both sanitize to %q", existing, ref, sanitized)
		}
		refMap[sanitized] = ref
	}

	return defs, builtins, refMap, maxIter, nil
}

// sanitizeToolName converts a tool ref (e.g. "search@v1") to a name that is safe
// for LLM tool name fields: replaces "@" with "__" and any remaining characters
// outside [a-zA-Z0-9_-] with "_".
func sanitizeToolName(ref string) string {
	s := strings.ReplaceAll(ref, "@", "__")
	return strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-' {
			return r
		}
		return '_'
	}, s)
}

// buildCompletionRequest assembles the provider-agnostic CompletionRequest for a prompt node.
func buildCompletionRequest(
	node bundle.Node,
	nodeDir string,
	inputs map[string]any,
	model string,
) (CompletionRequest, error) {
	maxTokens := 16000
	if v, ok, err := configInt(node.Config, "max_tokens"); err != nil {
		return CompletionRequest{}, fmt.Errorf("max_tokens: %w", err)
	} else if ok {
		if v <= 0 {
			return CompletionRequest{}, fmt.Errorf("config.max_tokens must be a positive integer, got %d", v)
		}
		maxTokens = v
	}

	req := CompletionRequest{
		Model:     model,
		MaxTokens: maxTokens,
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

	// --- Optional thinking_budget ---
	if v, ok, err := configInt(node.Config, "thinking_budget"); err != nil {
		return req, fmt.Errorf("thinking_budget: %w", err)
	} else if ok {
		if v < 0 {
			return req, fmt.Errorf("config.thinking_budget must be >= 0, got %d", v)
		}
		req.ThinkingBudget = &v
	}

	// --- Optional reasoning_effort ---
	if raw, ok := node.Config["reasoning_effort"]; ok {
		var effort string
		if err := json.Unmarshal(raw, &effort); err != nil {
			return req, fmt.Errorf("config.reasoning_effort must be a string: %w", err)
		}
		switch effort {
		case "low", "medium", "high":
			req.ReasoningEffort = &effort
		default:
			return req, fmt.Errorf("config.reasoning_effort must be \"low\", \"medium\", or \"high\", got %q", effort)
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

// collectFileBlocks extracts FileValue and ToolImageOutput inputs and returns them as ContentBlocks.
func collectFileBlocks(inputs map[string]any) []ContentBlock {
	var blocks []ContentBlock
	for _, v := range inputs {
		switch tv := v.(type) {
		case FileValue:
			kind := "document"
			if strings.HasPrefix(tv.MediaType, "image/") {
				kind = "image"
			}
			blocks = append(blocks, ContentBlock{
				Type:      kind,
				Data:      tv.Data,
				MediaType: tv.MediaType,
			})
		case ToolImageOutput:
			blocks = append(blocks, ContentBlock{
				Type:      "image",
				Data:      tv.Data,
				MediaType: tv.MediaType,
			})
		}
	}
	return blocks
}

// buildToolResultContent splits a tool output map into a text part (JSON of non-image
// fields) and image sub-blocks. When there are no images the caller should use the
// text part directly on ContentBlock.Text; when there are images the caller should
// populate ContentBlock.SubBlocks with the returned slice.
func buildToolResultContent(output map[string]any) (textPart string, subBlocks []ContentBlock) {
	remaining := make(map[string]any, len(output))
	for k, v := range output {
		if img, ok := v.(ToolImageOutput); ok {
			subBlocks = append(subBlocks, ContentBlock{
				Type:      "image",
				Data:      img.Data,
				MediaType: img.MediaType,
			})
		} else {
			remaining[k] = v
		}
	}
	if len(subBlocks) == 0 {
		b, _ := json.Marshal(output)
		return string(b), nil
	}
	// Build sub-blocks: text first (if there are non-image fields), then images.
	if len(remaining) > 0 {
		b, _ := json.Marshal(remaining)
		text := string(b)
		if text != "{}" {
			subBlocks = append([]ContentBlock{{Type: "text", Text: text}}, subBlocks...)
		}
	}
	return "", subBlocks
}

// sanitizeOutputForTrace replaces ToolImageOutput values with a compact summary
// so trace files don't contain raw image bytes.
func sanitizeOutputForTrace(output map[string]any) map[string]any {
	result := make(map[string]any, len(output))
	for k, v := range output {
		if img, ok := v.(ToolImageOutput); ok {
			result[k] = map[string]any{"type": "image", "mediaType": img.MediaType, "size": len(img.Data)}
		} else {
			result[k] = v
		}
	}
	return result
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

// requestToTrace converts a CompletionRequest into a map suitable for trace Inputs.
// Binary data (images, documents) is replaced with a compact size summary.
func requestToTrace(req CompletionRequest) map[string]any {
	m := map[string]any{
		"messages": messagesToTrace(req.Messages),
	}
	if req.System != "" {
		m["system"] = req.System
	}
	if req.Temperature != nil {
		m["temperature"] = *req.Temperature
	}
	if req.ThinkingBudget != nil {
		m["thinking_budget"] = *req.ThinkingBudget
	}
	if req.ReasoningEffort != nil {
		m["reasoning_effort"] = *req.ReasoningEffort
	}
	return m
}

// messagesToTrace converts a []Message to a JSON-serializable slice, replacing
// binary content blocks with lightweight summaries.
func messagesToTrace(msgs []Message) []map[string]any {
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		entry := map[string]any{"role": m.Role}
		if len(m.Blocks) == 0 {
			entry["content"] = m.Content
		} else {
			parts := make([]map[string]any, 0, len(m.Blocks))
			for _, b := range m.Blocks {
				switch b.Type {
				case "text":
					parts = append(parts, map[string]any{"type": "text", "text": b.Text})
				case "tool_use":
					var args any
					_ = json.Unmarshal(b.ToolInput, &args)
					parts = append(parts, map[string]any{"type": "tool_use", "id": b.ToolUseID, "tool": b.ToolName, "input": args})
				case "tool_result":
					if len(b.SubBlocks) > 0 {
						parts = append(parts, map[string]any{"type": "tool_result", "id": b.ToolUseID, "tool": b.ToolName, "content": "[multimodal]"})
					} else {
						parts = append(parts, map[string]any{"type": "tool_result", "id": b.ToolUseID, "tool": b.ToolName, "content": b.Text})
					}
				case "image", "document":
					parts = append(parts, map[string]any{"type": b.Type, "media_type": b.MediaType, "size_bytes": len(b.Data)})
				}
			}
			entry["content"] = parts
		}
		out = append(out, entry)
	}
	return out
}
