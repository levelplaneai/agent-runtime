package runtime

import (
	"context"
	"fmt"
	"strings"
)

// ProviderRegistry implements LLMProvider by routing requests to named sub-providers
// based on a "provider/model" prefix in CompletionRequest.Model.
//
// Example: "openai/gpt-4o" routes to the "openai" provider with model "gpt-4o".
// Bare model names (no slash) are routed to the defaultProvider.
type ProviderRegistry struct {
	providers       map[string]LLMProvider
	defaultProvider string
}

// NewProviderRegistry creates a registry that routes bare model names to defaultProvider.
func NewProviderRegistry(defaultProvider string) *ProviderRegistry {
	return &ProviderRegistry{
		providers:       make(map[string]LLMProvider),
		defaultProvider: defaultProvider,
	}
}

// Register adds a named provider to the registry.
func (r *ProviderRegistry) Register(name string, p LLMProvider) {
	r.providers[name] = p
}

// Complete routes the request to the appropriate provider and strips the prefix from
// the model name before forwarding.
func (r *ProviderRegistry) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	providerName, modelName := parseModel(req.Model, r.defaultProvider)
	p, ok := r.providers[providerName]
	if !ok {
		return CompletionResponse{}, fmt.Errorf("provider %q not registered (model: %q)", providerName, req.Model)
	}
	req.Model = modelName
	return p.Complete(ctx, req)
}

// parseModel splits "provider/model" into (provider, model).
// If there is no slash the original string is returned as the model name and
// defaultProvider is used as the provider.
func parseModel(model, defaultProvider string) (provider, modelName string) {
	if idx := strings.IndexByte(model, '/'); idx != -1 {
		return model[:idx], model[idx+1:]
	}
	return defaultProvider, model
}
