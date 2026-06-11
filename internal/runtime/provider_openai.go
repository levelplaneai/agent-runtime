package runtime

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/shared"
)

// OpenAIProvider implements LLMProvider using the OpenAI Chat Completions API.
// It also works with any OpenAI-compatible API (Groq, Together, Ollama, etc.)
// by configuring the client's base URL.
type OpenAIProvider struct {
	client *openai.Client
}

// NewOpenAIProvider wraps an OpenAI SDK client.
func NewOpenAIProvider(client *openai.Client) *OpenAIProvider {
	return &OpenAIProvider{client: client}
}

func (p *OpenAIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if len(req.BuiltinTools) > 0 {
		return CompletionResponse{}, fmt.Errorf("openai: provider-managed built-in tools are not supported in the Chat Completions API")
	}

	params := openai.ChatCompletionNewParams{
		Model: openai.ChatModel(req.Model),
	}
	if isOSeriesModel(req.Model) {
		params.MaxCompletionTokens = openai.Int(int64(req.MaxTokens))
	} else {
		params.MaxTokens = openai.Int(int64(req.MaxTokens))
	}

	var msgs []openai.ChatCompletionMessageParamUnion
	if req.System != "" {
		msgs = append(msgs, openai.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		encoded, err := openaiEncodeMessage(m)
		if err != nil {
			return CompletionResponse{}, err
		}
		msgs = append(msgs, encoded...)
	}
	params.Messages = msgs

	if req.OutputSchema != nil {
		params.ResponseFormat = openai.ChatCompletionNewParamsResponseFormatUnion{
			OfJSONSchema: &shared.ResponseFormatJSONSchemaParam{
				JSONSchema: shared.ResponseFormatJSONSchemaJSONSchemaParam{
					Name:   "response",
					Schema: req.OutputSchema,
					Strict: openai.Bool(true),
				},
			},
		}
	}

	if req.Temperature != nil {
		params.Temperature = openai.Float(*req.Temperature)
	}

	if req.ReasoningEffort != nil && isOSeriesModel(req.Model) {
		switch *req.ReasoningEffort {
		case "low":
			params.ReasoningEffort = shared.ReasoningEffortLow
		case "medium":
			params.ReasoningEffort = shared.ReasoningEffortMedium
		case "high":
			params.ReasoningEffort = shared.ReasoningEffortHigh
		}
	}

	if len(req.Tools) > 0 {
		oaiTools := make([]openai.ChatCompletionToolParam, len(req.Tools))
		for i, td := range req.Tools {
			oaiTools[i] = openai.ChatCompletionToolParam{
				Function: shared.FunctionDefinitionParam{
					Name:        td.Name,
					Description: openai.String(td.Description),
					Parameters:  shared.FunctionParameters(td.InputSchema),
				},
			}
		}
		params.Tools = oaiTools
	}

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("openai: API call failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("openai: no choices in response")
	}

	choice := resp.Choices[0]

	if choice.FinishReason == "tool_calls" {
		var toolCalls []ToolCall
		for _, tc := range choice.Message.ToolCalls {
			toolCalls = append(toolCalls, ToolCall{
				ID:    tc.ID,
				Name:  tc.Function.Name,
				Input: json.RawMessage(tc.Function.Arguments),
			})
		}
		return CompletionResponse{
			Content:      choice.Message.Content,
			ToolCalls:    toolCalls,
			StopReason:   "tool_use",
			InputTokens:  resp.Usage.PromptTokens,
			OutputTokens: resp.Usage.CompletionTokens,
		}, nil
	}

	return CompletionResponse{
		Content:      choice.Message.Content,
		StopReason:   "end_turn",
		InputTokens:  resp.Usage.PromptTokens,
		OutputTokens: resp.Usage.CompletionTokens,
	}, nil
}

// openaiEncodeMessage converts a Message to one or more OpenAI message params.
// Most messages produce a single param, but a user message with tool_result blocks
// must be split into one ChatCompletionToolMessageParam per result (OpenAI requirement).
func openaiEncodeMessage(m Message) ([]openai.ChatCompletionMessageParamUnion, error) {
	role := strings.ToLower(m.Role)

	switch role {
	case "user":
		if m.Blocks != nil {
			// Tool result blocks require individual tool messages.
			if hasBlockType(m.Blocks, "tool_result") {
				var out []openai.ChatCompletionMessageParamUnion
				for _, b := range m.Blocks {
					if b.Type == "tool_result" {
						out = append(out, openai.ToolMessage(b.Text, b.ToolUseID))
					}
				}
				return out, nil
			}
			parts, err := openaiContentParts(m.Blocks)
			if err != nil {
				return nil, err
			}
			return []openai.ChatCompletionMessageParamUnion{openai.UserMessage(parts)}, nil
		}
		return []openai.ChatCompletionMessageParamUnion{openai.UserMessage(m.Content)}, nil

	case "assistant":
		if m.Blocks != nil && hasBlockType(m.Blocks, "tool_use") {
			// Build an assistant message with tool_calls.
			var toolCalls []openai.ChatCompletionMessageToolCallParam
			var textContent string
			for _, b := range m.Blocks {
				switch b.Type {
				case "text":
					textContent = b.Text
				case "tool_use":
					toolCalls = append(toolCalls, openai.ChatCompletionMessageToolCallParam{
						ID: b.ToolUseID,
						Function: openai.ChatCompletionMessageToolCallFunctionParam{
							Name:      b.ToolName,
							Arguments: string(b.ToolInput),
						},
					})
				}
			}
			asst := &openai.ChatCompletionAssistantMessageParam{ToolCalls: toolCalls}
			if textContent != "" {
				asst.Content = openai.ChatCompletionAssistantMessageParamContentUnion{
					OfString: openai.String(textContent),
				}
			}
			return []openai.ChatCompletionMessageParamUnion{{OfAssistant: asst}}, nil
		}
		return []openai.ChatCompletionMessageParamUnion{openai.AssistantMessage(m.Content)}, nil

	default:
		return nil, fmt.Errorf("openai: unknown role %q", m.Role)
	}
}

// hasBlockType reports whether any block in the slice has the given type.
func hasBlockType(blocks []ContentBlock, typ string) bool {
	for _, b := range blocks {
		if b.Type == typ {
			return true
		}
	}
	return false
}

// isOSeriesModel reports whether a bare model name is an OpenAI o-series reasoning
// model (o1, o3, o4-mini, etc.) that uses MaxCompletionTokens instead of MaxTokens
// and supports ReasoningEffort.
func isOSeriesModel(model string) bool {
	return len(model) >= 2 && model[0] == 'o' && model[1] >= '1' && model[1] <= '9'
}

// openaiContentParts converts ContentBlocks to OpenAI chat content parts.
// Images are sent as base64 data URLs. Documents (PDFs, etc.) are not supported
// by the OpenAI chat API and return an error.
func openaiContentParts(blocks []ContentBlock) ([]openai.ChatCompletionContentPartUnionParam, error) {
	parts := make([]openai.ChatCompletionContentPartUnionParam, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			parts = append(parts, openai.TextContentPart(b.Text))
		case "image":
			encoded := base64.StdEncoding.EncodeToString(b.Data)
			dataURL := "data:" + b.MediaType + ";base64," + encoded
			parts = append(parts, openai.ImageContentPart(openai.ChatCompletionContentPartImageImageURLParam{
				URL: dataURL,
			}))
		case "document":
			return nil, fmt.Errorf("openai: document inputs are not supported; use the Anthropic provider or convert to an image")
		default:
			return nil, fmt.Errorf("openai: unsupported content block type %q", b.Type)
		}
	}
	return parts, nil
}
