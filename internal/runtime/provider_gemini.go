package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/genai"
)

// GeminiProvider implements LLMProvider using the Google GenAI API (Gemini models).
type GeminiProvider struct {
	client *genai.Client
}

// NewGeminiProvider wraps a Google GenAI client.
func NewGeminiProvider(client *genai.Client) *GeminiProvider {
	return &GeminiProvider{client: client}
}

func (p *GeminiProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	cfg := &genai.GenerateContentConfig{}

	if req.System != "" {
		cfg.SystemInstruction = &genai.Content{
			Parts: []*genai.Part{{Text: req.System}},
		}
	}

	if req.MaxTokens > 0 {
		cfg.MaxOutputTokens = int32(req.MaxTokens)
	}

	if req.Temperature != nil {
		t := float32(*req.Temperature)
		cfg.Temperature = &t
	}

	if req.OutputSchema != nil {
		cfg.ResponseMIMEType = "application/json"
		schema, err := mapToGeminiSchema(req.OutputSchema)
		if err == nil {
			cfg.ResponseSchema = schema
		}
	}

	if len(req.Tools) > 0 || len(req.BuiltinTools) > 0 {
		tool := &genai.Tool{}
		if len(req.Tools) > 0 {
			decls := make([]*genai.FunctionDeclaration, len(req.Tools))
			for i, td := range req.Tools {
				decls[i] = &genai.FunctionDeclaration{
					Name:                 td.Name,
					Description:          td.Description,
					ParametersJsonSchema: td.InputSchema,
				}
			}
			tool.FunctionDeclarations = decls
		}
		for _, bt := range req.BuiltinTools {
			switch bt.Name {
			case "gemini:code_execution":
				tool.CodeExecution = &genai.ToolCodeExecution{}
			case "gemini:google_search":
				tool.GoogleSearch = &genai.GoogleSearch{}
			case "gemini:url_context":
				tool.URLContext = &genai.URLContext{}
			}
		}
		cfg.Tools = []*genai.Tool{tool}
	}

	contents, err := buildGeminiContents(req.Messages)
	if err != nil {
		return CompletionResponse{}, err
	}

	resp, err := p.client.Models.GenerateContent(ctx, req.Model, contents, cfg)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("gemini: API call failed: %w", err)
	}

	if len(resp.Candidates) == 0 {
		return CompletionResponse{}, fmt.Errorf("gemini: no candidates in response")
	}
	cand := resp.Candidates[0]
	if cand.Content == nil {
		return CompletionResponse{}, fmt.Errorf("gemini: no content in candidate")
	}

	var textContent string
	var toolCalls []ToolCall
	var builtinToolsUsed []string
	for idx, part := range cand.Content.Parts {
		if part.Text != "" {
			textContent += part.Text
		}
		if fc := part.FunctionCall; fc != nil {
			inputBytes, _ := json.Marshal(fc.Args)
			id := fc.ID
			if id == "" {
				id = fmt.Sprintf("gemini-%s-%d", fc.Name, idx)
			}
			toolCalls = append(toolCalls, ToolCall{
				ID:    id,
				Name:  fc.Name,
				Input: json.RawMessage(inputBytes),
			})
		}
		if part.CodeExecutionResult != nil {
			builtinToolsUsed = appendUnique(builtinToolsUsed, "gemini:code_execution")
		}
	}
	// Google Search grounding metadata is on the candidate, not in parts.
	if cand.GroundingMetadata != nil {
		builtinToolsUsed = appendUnique(builtinToolsUsed, "gemini:google_search")
	}

	if len(toolCalls) > 0 {
		return CompletionResponse{
			Content:          textContent,
			ToolCalls:        toolCalls,
			StopReason:       "tool_use",
			BuiltinToolsUsed: builtinToolsUsed,
		}, nil
	}

	if textContent == "" {
		return CompletionResponse{}, fmt.Errorf("gemini: no text or function calls in response")
	}
	return CompletionResponse{Content: textContent, StopReason: "end_turn", BuiltinToolsUsed: builtinToolsUsed}, nil
}


func buildGeminiContents(messages []Message) ([]*genai.Content, error) {
	contents := make([]*genai.Content, 0, len(messages))
	for _, m := range messages {
		var role string
		switch strings.ToLower(m.Role) {
		case "user":
			role = genai.RoleUser
		case "assistant":
			role = genai.RoleModel
		default:
			return nil, fmt.Errorf("gemini: unknown role %q", m.Role)
		}

		var parts []*genai.Part
		if m.Blocks != nil {
			for _, b := range m.Blocks {
				switch b.Type {
				case "text":
					parts = append(parts, &genai.Part{Text: b.Text})
				case "image", "document":
					parts = append(parts, &genai.Part{
						InlineData: &genai.Blob{MIMEType: b.MediaType, Data: b.Data},
					})
				case "tool_use":
					var args map[string]any
					_ = json.Unmarshal(b.ToolInput, &args)
					parts = append(parts, genai.NewPartFromFunctionCall(b.ToolName, args))
				case "tool_result":
					var response map[string]any
					if err := json.Unmarshal([]byte(b.Text), &response); err != nil {
						if b.IsError {
							response = map[string]any{"error": b.Text}
						} else {
							response = map[string]any{"output": b.Text}
						}
					}
					var responseParts []*genai.FunctionResponsePart
					for _, sub := range b.SubBlocks {
						if sub.Type == "image" {
							responseParts = append(responseParts, genai.NewFunctionResponsePartFromBytes(sub.Data, sub.MediaType))
						}
					}
					if len(responseParts) == 0 {
						parts = append(parts, genai.NewPartFromFunctionResponse(b.ToolName, response))
					} else {
						parts = append(parts, genai.NewPartFromFunctionResponseWithParts(b.ToolName, response, responseParts))
					}
				default:
					return nil, fmt.Errorf("gemini: unsupported content block type %q", b.Type)
				}
			}
		} else {
			parts = []*genai.Part{{Text: m.Content}}
		}

		contents = append(contents, &genai.Content{Role: role, Parts: parts})
	}
	return contents, nil
}

// mapToGeminiSchema converts a map[string]any JSON schema to *genai.Schema via
// round-trip through JSON.
func mapToGeminiSchema(schema map[string]any) (*genai.Schema, error) {
	b, err := json.Marshal(schema)
	if err != nil {
		return nil, err
	}
	var s genai.Schema
	if err := json.Unmarshal(b, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
