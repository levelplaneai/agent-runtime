package runtime

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
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

// retryBackoff is the wait schedule for transient provider failures. Package
// variable so tests can shrink it.
var retryBackoff = []time.Duration{2 * time.Second, 8 * time.Second, 20 * time.Second}

// Complete routes the request to the appropriate provider and strips the prefix from
// the model name before forwarding.
//
// Transient provider-side failures (rate limits, overload, upstream 5xx,
// dropped connections) are retried on a short backoff before the error is
// allowed to fail the node. Without this, a single Anthropic 529
// "overloaded_error" mid-flow kills an entire multi-node run that may already
// be tens of minutes in. Sitting in the registry, the retry covers every LLM
// call site (prompt nodes, agentic loops, LLM routers) in one place.
func (r *ProviderRegistry) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	providerName, modelName := parseModel(req.Model, r.defaultProvider)
	p, ok := r.providers[providerName]
	if !ok {
		return CompletionResponse{}, fmt.Errorf("provider %q not registered (model: %q)", providerName, req.Model)
	}
	req.Model = modelName

	var resp CompletionResponse
	var err error
	for attempt := 0; ; attempt++ {
		resp, err = p.Complete(ctx, req)
		if err == nil || attempt >= len(retryBackoff) || !isTransient(err) {
			return resp, err
		}
		fmt.Fprintf(os.Stderr, "provider %s: transient error, retry %d/%d in %s: %v\n",
			providerName, attempt+1, len(retryBackoff), retryBackoff[attempt], err)
		select {
		case <-ctx.Done():
			return resp, err
		case <-time.After(retryBackoff[attempt]):
		}
	}
}

// isTransient reports whether a provider error is worth retrying. The three
// SDKs surface errors as formatted strings, so this is marker matching on the
// message — crude, but provider-agnostic. A false positive merely re-attempts
// a doomed call a few times; a false negative fails the node as before.
func isTransient(err error) bool {
	s := strings.ToLower(err.Error())
	for _, marker := range []string{
		" 408", " 429", " 500", " 502", " 503", " 504", " 529",
		"overloaded", "rate limit", "rate_limit",
		"resource_exhausted", "unavailable", "internal server error",
		"connection reset", "unexpected eof", "i/o timeout",
	} {
		if strings.Contains(s, marker) {
			return true
		}
	}
	return false
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
