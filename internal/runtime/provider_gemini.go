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

	contents, err := buildGeminiContents(req.Messages)
	if err != nil {
		return CompletionResponse{}, err
	}

	resp, err := p.client.Models.GenerateContent(ctx, req.Model, contents, cfg)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("gemini: API call failed: %w", err)
	}
	text := resp.Text()
	if text == "" {
		return CompletionResponse{}, fmt.Errorf("gemini: no text in response")
	}
	return CompletionResponse{Content: text}, nil
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
