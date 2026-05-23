package runtime

import (
	"context"
	"encoding/base64"
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
	params := openai.ChatCompletionNewParams{
		Model:     openai.ChatModel(req.Model),
		MaxTokens: openai.Int(int64(req.MaxTokens)),
	}

	var msgs []openai.ChatCompletionMessageParamUnion
	if req.System != "" {
		msgs = append(msgs, openai.SystemMessage(req.System))
	}
	for _, m := range req.Messages {
		switch strings.ToLower(m.Role) {
		case "user":
			if m.Blocks != nil {
				parts, err := openaiContentParts(m.Blocks)
				if err != nil {
					return CompletionResponse{}, err
				}
				msgs = append(msgs, openai.UserMessage(parts))
			} else {
				msgs = append(msgs, openai.UserMessage(m.Content))
			}
		case "assistant":
			msgs = append(msgs, openai.AssistantMessage(m.Content))
		default:
			return CompletionResponse{}, fmt.Errorf("openai: unknown role %q", m.Role)
		}
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

	resp, err := p.client.Chat.Completions.New(ctx, params)
	if err != nil {
		return CompletionResponse{}, fmt.Errorf("openai: API call failed: %w", err)
	}
	if len(resp.Choices) == 0 {
		return CompletionResponse{}, fmt.Errorf("openai: no choices in response")
	}
	return CompletionResponse{Content: resp.Choices[0].Message.Content}, nil
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
