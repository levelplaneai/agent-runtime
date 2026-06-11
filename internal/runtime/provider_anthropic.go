package runtime

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

// AnthropicProvider implements LLMProvider using the Anthropic Messages API.
type AnthropicProvider struct {
	client *anthropic.Client
}

// NewAnthropicProvider wraps an Anthropic SDK client.
func NewAnthropicProvider(client *anthropic.Client) *AnthropicProvider {
	return &AnthropicProvider{client: client}
}

func (p *AnthropicProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(req.Model),
		MaxTokens: int64(req.MaxTokens),
	}

	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}

	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		blocks, err := anthropicBlocks(m)
		if err != nil {
			return CompletionResponse{}, err
		}
		switch strings.ToLower(m.Role) {
		case "user":
			msgs = append(msgs, anthropic.NewUserMessage(blocks...))
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(blocks...))
		default:
			return CompletionResponse{}, fmt.Errorf("anthropic: unknown role %q", m.Role)
		}
	}
	params.Messages = msgs

	if req.OutputSchema != nil {
		params.OutputConfig = anthropic.OutputConfigParam{
			Format: anthropic.JSONOutputFormatParam{Schema: enforceAdditionalProperties(req.OutputSchema)},
		}
	}

	if req.ThinkingBudget != nil {
		b := *req.ThinkingBudget
		if b == 0 {
			params.Thinking = anthropic.ThinkingConfigParamUnion{
				OfDisabled: &anthropic.ThinkingConfigDisabledParam{},
			}
		} else {
			if b < 1024 {
				return CompletionResponse{}, fmt.Errorf("anthropic: thinking_budget must be >= 1024, got %d", b)
			}
			if b >= req.MaxTokens {
				return CompletionResponse{}, fmt.Errorf("anthropic: thinking_budget (%d) must be less than max_tokens (%d)", b, req.MaxTokens)
			}
			params.Thinking = anthropic.ThinkingConfigParamUnion{
				OfEnabled: &anthropic.ThinkingConfigEnabledParam{BudgetTokens: int64(b)},
			}
		}
	}

	if req.Temperature != nil {
		// Extended thinking requires temperature to be unset.
		if params.Thinking.OfEnabled == nil {
			params.Temperature = anthropic.Float(*req.Temperature)
		}
	}

	if len(req.Tools) > 0 || len(req.BuiltinTools) > 0 {
		tools := make([]anthropic.ToolUnionParam, 0, len(req.Tools)+len(req.BuiltinTools))
		for _, td := range req.Tools {
			schema := anthropicToolInputSchema(td.InputSchema)
			t := anthropic.ToolUnionParamOfTool(schema, td.Name)
			t.OfTool.Description = anthropic.String(td.Description)
			tools = append(tools, t)
		}
		for _, bt := range req.BuiltinTools {
			switch bt.Name {
			case "anthropic:web_search":
				tools = append(tools, anthropic.ToolUnionParam{OfWebSearchTool20260209: &anthropic.WebSearchTool20260209Param{}})
			case "anthropic:code_execution":
				tools = append(tools, anthropic.ToolUnionParam{OfCodeExecutionTool20260120: &anthropic.CodeExecutionTool20260120Param{}})
			case "anthropic:bash":
				tools = append(tools, anthropic.ToolUnionParam{OfBashTool20250124: &anthropic.ToolBash20250124Param{}})
			case "anthropic:text_editor":
				tools = append(tools, anthropic.ToolUnionParam{OfTextEditor20250728: &anthropic.ToolTextEditor20250728Param{}})
			}
		}
		params.Tools = tools
	}

	resp, err := p.client.Messages.New(ctx, params)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("anthropic: API call failed: %w", err)
	}

	var textContent string
	var toolCalls []ToolCall
	var builtinToolsUsed []string
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			textContent += v.Text
		case anthropic.ToolUseBlock:
			toolCalls = append(toolCalls, ToolCall{
				ID:    v.ID,
				Name:  v.Name,
				Input: v.Input,
			})
		case anthropic.WebSearchToolResultBlock:
			_ = v
			builtinToolsUsed = appendUnique(builtinToolsUsed, "anthropic:web_search")
		case anthropic.WebFetchToolResultBlock:
			_ = v
			builtinToolsUsed = appendUnique(builtinToolsUsed, "anthropic:web_fetch")
		case anthropic.CodeExecutionToolResultBlock:
			_ = v
			builtinToolsUsed = appendUnique(builtinToolsUsed, "anthropic:code_execution")
		case anthropic.BashCodeExecutionToolResultBlock:
			_ = v
			builtinToolsUsed = appendUnique(builtinToolsUsed, "anthropic:bash")
		case anthropic.TextEditorCodeExecutionToolResultBlock:
			_ = v
			builtinToolsUsed = appendUnique(builtinToolsUsed, "anthropic:text_editor")
		}
	}

	stopReason := string(resp.StopReason)
	if stopReason == "" {
		stopReason = "end_turn"
	}

	if len(toolCalls) > 0 {
		return CompletionResponse{
			Content:          textContent,
			ToolCalls:        toolCalls,
			StopReason:       "tool_use",
			InputTokens:      resp.Usage.InputTokens,
			OutputTokens:     resp.Usage.OutputTokens,
			BuiltinToolsUsed: builtinToolsUsed,
		}, nil
	}

	if textContent == "" {
		return CompletionResponse{}, fmt.Errorf("anthropic: no text block in response (stop_reason: %s)", resp.StopReason)
	}
	return CompletionResponse{
		Content:          textContent,
		StopReason:       stopReason,
		InputTokens:      resp.Usage.InputTokens,
		OutputTokens:     resp.Usage.OutputTokens,
		BuiltinToolsUsed: builtinToolsUsed,
	}, nil
}


// anthropicToolInputSchema converts a map[string]any JSON Schema to an Anthropic
// ToolInputSchemaParam. Properties and required are placed in their typed struct fields;
// all other schema keywords go into ExtraFields for pass-through marshaling.
func anthropicToolInputSchema(schema map[string]any) anthropic.ToolInputSchemaParam {
	result := anthropic.ToolInputSchemaParam{
		ExtraFields: make(map[string]any),
	}
	for k, v := range schema {
		switch k {
		case "type":
			// skip — ToolInputSchemaParam always marshals type as "object"
		case "properties":
			result.Properties = v
		case "required":
			if reqSlice, ok := v.([]any); ok {
				strs := make([]string, 0, len(reqSlice))
				for _, r := range reqSlice {
					if s, ok := r.(string); ok {
						strs = append(strs, s)
					}
				}
				result.Required = strs
			}
		default:
			result.ExtraFields[k] = v
		}
	}
	return result
}

// enforceAdditionalProperties recursively sets "additionalProperties": false on
// every object schema node. Anthropic's structured output API requires this on
// all object types.
func enforceAdditionalProperties(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for k, v := range schema {
		out[k] = v
	}

	if out["type"] == "object" {
		if _, exists := out["additionalProperties"]; !exists {
			out["additionalProperties"] = false
		}
		if props, ok := out["properties"].(map[string]any); ok {
			newProps := make(map[string]any, len(props))
			for k, v := range props {
				if sub, ok := v.(map[string]any); ok {
					newProps[k] = enforceAdditionalProperties(sub)
				} else {
					newProps[k] = v
				}
			}
			out["properties"] = newProps
		}
	}

	if items, ok := out["items"].(map[string]any); ok {
		out["items"] = enforceAdditionalProperties(items)
	}

	return out
}

// anthropicBlocks converts a Message to Anthropic content block params.
// For plain text messages (Blocks == nil) it returns a single text block.
// For multimodal messages it maps each ContentBlock to the appropriate Anthropic type.
func anthropicBlocks(m Message) ([]anthropic.ContentBlockParamUnion, error) {
	if m.Blocks == nil {
		return []anthropic.ContentBlockParamUnion{anthropic.NewTextBlock(m.Content)}, nil
	}

	out := make([]anthropic.ContentBlockParamUnion, 0, len(m.Blocks))
	for _, b := range m.Blocks {
		switch b.Type {
		case "text":
			out = append(out, anthropic.NewTextBlock(b.Text))
		case "image":
			encoded := base64.StdEncoding.EncodeToString(b.Data)
			out = append(out, anthropic.NewImageBlockBase64(b.MediaType, encoded))
		case "document":
			encoded := base64.StdEncoding.EncodeToString(b.Data)
			if b.MediaType == "application/pdf" {
				out = append(out, anthropic.NewDocumentBlock(anthropic.Base64PDFSourceParam{
					Data: encoded,
				}))
			} else {
				// Plain-text documents (markdown, CSV, HTML, etc.) sent as text source.
				out = append(out, anthropic.NewDocumentBlock(anthropic.PlainTextSourceParam{
					Data: string(b.Data),
				}))
			}
		case "tool_use":
			out = append(out, anthropic.NewToolUseBlock(b.ToolUseID, b.ToolInput, b.ToolName))
		case "tool_result":
			toolBlock := anthropic.ToolResultBlockParam{
				ToolUseID: b.ToolUseID,
				IsError:   anthropic.Bool(b.IsError),
			}
			if len(b.SubBlocks) == 0 {
				toolBlock.Content = []anthropic.ToolResultBlockParamContentUnion{
					{OfText: &anthropic.TextBlockParam{Text: b.Text}},
				}
			} else {
				for _, sub := range b.SubBlocks {
					switch sub.Type {
					case "text":
						toolBlock.Content = append(toolBlock.Content, anthropic.ToolResultBlockParamContentUnion{
							OfText: &anthropic.TextBlockParam{Text: sub.Text},
						})
					case "image":
						encoded := base64.StdEncoding.EncodeToString(sub.Data)
						toolBlock.Content = append(toolBlock.Content, anthropic.ToolResultBlockParamContentUnion{
							OfImage: &anthropic.ImageBlockParam{
								Source: anthropic.ImageBlockParamSourceUnion{
									OfBase64: &anthropic.Base64ImageSourceParam{
										Data:      encoded,
										MediaType: anthropic.Base64ImageSourceMediaType(sub.MediaType),
									},
								},
							},
						})
					}
				}
			}
			out = append(out, anthropic.ContentBlockParamUnion{OfToolResult: &toolBlock})
		default:
			return nil, fmt.Errorf("anthropic: unsupported content block type %q", b.Type)
		}
	}
	return out, nil
}

