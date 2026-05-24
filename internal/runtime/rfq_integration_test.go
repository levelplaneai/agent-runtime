//go:build integration

package runtime

import (
	"context"
	"os"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// TestRFQProcessor_Integration runs the rfq_processor bundle end-to-end.
// It registers a stub supplier_api.get_price@v1 that returns a fixed price
// so no real supplier API is needed, but prompt nodes hit a real Anthropic model.
//
// Run with:
//
//	ANTHROPIC_API_KEY=sk-... go test ./internal/runtime/ -tags integration -run TestRFQProcessor -v
func TestRFQProcessor_Integration(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	b, err := bundle.Load("../../testdata/rfq_processor.agent")
	if err != nil {
		t.Fatalf("load bundle: %v", err)
	}
	if errs := bundle.Validate(b); len(errs) > 0 {
		for _, e := range errs {
			t.Logf("validation error: %v", e)
		}
		t.Fatal("bundle validation failed")
	}

	reg := NewRegistry()
	reg.Register("supplier_api.get_price@v1", bundle.ToolSignature{}, ToolFunc(
		func(_ context.Context, args map[string]any) (map[string]any, error) {
			t.Logf("get_price called with args: %v", args)
			return map[string]any{"price": 42.00}, nil
		},
	))

	client := anthropic.NewClient()
	provider := NewAnthropicProvider(&client)

	tracer := NewTracer(nil, os.Stderr)
	ctx := ContextWithTracer(context.Background(), tracer)

	rfqDoc := `RFQ-2024-001
	Line items:
	1. Widget A (part# WGT-100), qty 10
	2. Bolt B (part# BLT-200), qty 50`

	out, err := RunFlow(ctx, b, map[string]any{"rfq_document": rfqDoc}, reg, provider)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}

	t.Logf("output: %#v", out)
	if _, ok := out["final_quote"]; !ok {
		t.Errorf("expected 'final_quote' in output, got keys: %v", keys(out))
	}
}

func keys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}
