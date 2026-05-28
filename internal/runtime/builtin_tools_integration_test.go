//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/levelplaneai/agent-runtime/internal/bundle"
	"google.golang.org/genai"
)

// TestAnthropicWebSearch_Integration verifies that a prompt node declaring
// "anthropic:web_search" fires the built-in tool and emits a builtin_tool_used
// trace event.
//
// Run with:
//
//	ANTHROPIC_API_KEY=sk-... go test ./internal/runtime/ -tags integration -run TestAnthropicWebSearch -v
func TestAnthropicWebSearch_Integration(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	b, err := bundle.Load("../../testdata/anthropic_builtin.agent")
	if err != nil {
		t.Fatalf("load bundle: %v", err)
	}
	if errs := bundle.Validate(b); len(errs) > 0 {
		for _, e := range errs {
			t.Logf("validation error: %v", e)
		}
		t.Fatal("bundle validation failed")
	}

	client := anthropic.NewClient()
	providerReg := NewProviderRegistry("anthropic")
	providerReg.Register("anthropic", NewAnthropicProvider(&client))

	var traceBuf bytes.Buffer
	tracer := NewTracer(&traceBuf, os.Stderr)
	ctx := ContextWithTracer(context.Background(), tracer)

	out, err := RunFlow(ctx, b, map[string]any{
		"question": "What year was the Eiffel Tower completed? Search the web to confirm.",
	}, nil, providerReg)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}

	t.Logf("output: %#v", out)

	// The flow output wraps the node output map under "answer".
	answer, ok := out["answer"].(map[string]any)
	if !ok {
		t.Fatalf("expected answer map in output, got %T: %v", out["answer"], out["answer"])
	}
	text, _ := answer["text"].(string)
	if text == "" {
		t.Error("expected non-empty text in answer")
	}
	t.Logf("answer text: %s", text)

	// Verify the builtin_tool_used trace event was emitted.
	sawBuiltinEvent := false
	for _, line := range strings.Split(traceBuf.String(), "\n") {
		if line == "" {
			continue
		}
		var ev TraceEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Event == "builtin_tool_used" && ev.Tool == "anthropic:web_search" {
			sawBuiltinEvent = true
			t.Logf("builtin_tool_used event: node=%s tool=%s attempt=%d", ev.Node, ev.Tool, ev.Attempt)
		}
	}
	if !sawBuiltinEvent {
		t.Error("expected builtin_tool_used event for anthropic:web_search in trace, got none")
		t.Logf("full trace:\n%s", traceBuf.String())
	}
}

// TestGeminiCodeExecution_Integration verifies that a prompt node declaring
// "gemini:code_execution" fires the built-in tool, produces the correct numeric
// answer, and emits a builtin_tool_used trace event.
//
// Run with:
//
//	GEMINI_API_KEY=... go test ./internal/runtime/ -tags integration -run TestGeminiCodeExecution -v
func TestGeminiCodeExecution_Integration(t *testing.T) {
	apiKey := os.Getenv("GEMINI_API_KEY")
	if apiKey == "" {
		t.Skip("GEMINI_API_KEY not set")
	}

	b, err := bundle.Load("../../testdata/gemini_builtin.agent")
	if err != nil {
		t.Fatalf("load bundle: %v", err)
	}
	if errs := bundle.Validate(b); len(errs) > 0 {
		for _, e := range errs {
			t.Logf("validation error: %v", e)
		}
		t.Fatal("bundle validation failed")
	}

	client, err := genai.NewClient(context.Background(), &genai.ClientConfig{APIKey: apiKey})
	if err != nil {
		t.Fatalf("create Gemini client: %v", err)
	}
	providerReg := NewProviderRegistry("gemini")
	providerReg.Register("gemini", NewGeminiProvider(client))

	var traceBuf bytes.Buffer
	tracer := NewTracer(&traceBuf, os.Stderr)
	ctx := ContextWithTracer(context.Background(), tracer)

	// sum(1..100) = 5050 — a simple deterministic result we can assert on.
	out, err := RunFlow(ctx, b, map[string]any{
		"problem": "Use Python to compute the sum of all integers from 1 to 100 inclusive. State the final answer clearly.",
	}, nil, providerReg)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}

	t.Logf("output: %#v", out)

	answer, ok := out["answer"].(map[string]any)
	if !ok {
		t.Fatalf("expected answer map in output, got %T: %v", out["answer"], out["answer"])
	}
	text, _ := answer["text"].(string)
	if text == "" {
		t.Error("expected non-empty text in answer")
	}
	t.Logf("answer text: %s", text)

	if !strings.Contains(text, "5050") {
		t.Errorf("expected answer to contain '5050', got: %s", text)
	}

	// Verify the builtin_tool_used trace event was emitted.
	sawBuiltinEvent := false
	for _, line := range strings.Split(traceBuf.String(), "\n") {
		if line == "" {
			continue
		}
		var ev TraceEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		if ev.Event == "builtin_tool_used" && ev.Tool == "gemini:code_execution" {
			sawBuiltinEvent = true
			t.Logf("builtin_tool_used event: node=%s tool=%s attempt=%d", ev.Node, ev.Tool, ev.Attempt)
		}
	}
	if !sawBuiltinEvent {
		t.Error("expected builtin_tool_used event for gemini:code_execution in trace, got none")
		t.Logf("full trace:\n%s", traceBuf.String())
	}
}
