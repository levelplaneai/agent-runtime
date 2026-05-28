//go:build integration

package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// TestAgenticToolCall_Integration verifies the agentic loop: a prompt node
// declares a registered tool, a real LLM decides to call it, the runtime
// executes the stub implementation, and the LLM incorporates the result into
// its final answer.
//
// Run with:
//
//	ANTHROPIC_API_KEY=sk-... go test ./internal/runtime/ -tags integration -run TestAgenticToolCall -v
func TestAgenticToolCall_Integration(t *testing.T) {
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Skip("ANTHROPIC_API_KEY not set")
	}

	b, err := bundle.Load("../../testdata/agentic_tool.agent")
	if err != nil {
		t.Fatalf("load bundle: %v", err)
	}
	if errs := bundle.Validate(b); len(errs) > 0 {
		for _, e := range errs {
			t.Logf("validation error: %v", e)
		}
		t.Fatal("bundle validation failed")
	}

	var toolCallCount atomic.Int32
	reg := NewRegistry()
	reg.Register("get_temperature@v1", bundle.ToolSignature{}, ToolFunc(
		func(_ context.Context, args map[string]any) (map[string]any, error) {
			toolCallCount.Add(1)
			t.Logf("get_temperature called with args: %v", args)
			return map[string]any{"temperature_f": 99}, nil
		},
	))

	client := anthropic.NewClient()
	providerReg := NewProviderRegistry("anthropic")
	providerReg.Register("anthropic", NewAnthropicProvider(&client))

	var traceBuf bytes.Buffer
	tracer := NewTracer(&traceBuf, os.Stderr)
	ctx := ContextWithTracer(context.Background(), tracer)

	out, err := RunFlow(ctx, b, map[string]any{"city": "TestCity"}, reg, providerReg, nil)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}
	t.Logf("output: %#v", out)

	// The stub returns 99°F — the LLM must have called the tool to know this.
	answer, ok := out["answer"].(map[string]any)
	if !ok {
		t.Fatalf("expected answer map in output, got %T: %v", out["answer"], out["answer"])
	}
	text, _ := answer["text"].(string)
	if text == "" {
		t.Error("expected non-empty text in answer")
	}
	t.Logf("answer text: %s", text)

	if !strings.Contains(text, "99") {
		t.Errorf("expected answer to mention 99 (the stubbed temperature), got: %s", text)
	}

	if toolCallCount.Load() == 0 {
		t.Error("expected get_temperature tool to be called at least once")
	}
	t.Logf("get_temperature called %d time(s)", toolCallCount.Load())

	// Verify tool_start and tool_done trace events were emitted.
	sawToolStart := false
	sawToolDone := false
	for _, line := range strings.Split(traceBuf.String(), "\n") {
		if line == "" {
			continue
		}
		var ev TraceEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			continue
		}
		switch ev.Event {
		case "tool_start":
			sawToolStart = true
			t.Logf("tool_start: node=%s tool=%s attempt=%d", ev.Node, ev.Tool, ev.Attempt)
		case "tool_done":
			sawToolDone = true
			t.Logf("tool_done: node=%s tool=%s attempt=%d", ev.Node, ev.Tool, ev.Attempt)
		}
	}
	if !sawToolStart {
		t.Error("expected tool_start trace event, got none")
		t.Logf("full trace:\n%s", traceBuf.String())
	}
	if !sawToolDone {
		t.Error("expected tool_done trace event, got none")
	}
}
