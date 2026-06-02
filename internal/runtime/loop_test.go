package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// makeLoopBundle builds a minimal bundle and flow wired for executeLoop tests.
// doNodeType lets callers inject different node types for the do-node.
func makeLoopBundle(t *testing.T, doNodeJSON string) (*bundle.Bundle, bundle.Flow) {
	t.Helper()
	b := &bundle.Bundle{
		Manifest: bundle.Manifest{Name: "loop_test", Entry: "main@v1"},
		Nodes: map[string]map[string]bundle.Node{
			"looper": {"v1": {Type: "loop", Config: map[string]json.RawMessage{
				"over":        json.RawMessage(`"$.inputs.items"`),
				"as":          json.RawMessage(`"item"`),
				"do":          json.RawMessage(`"worker"`),
				"append_from": json.RawMessage(`"new_items"`),
			}}},
			"worker": {"v1": mustUnmarshalNode(t, doNodeJSON)},
		},
		Flows: map[string]map[string]bundle.Flow{},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}
	flow := bundle.Flow{
		Entry: "looper",
		Nodes: map[string]string{
			"looper": "looper@v1",
			"worker": "worker@v1",
		},
	}
	return b, flow
}

func mustUnmarshalNode(t *testing.T, raw string) bundle.Node {
	t.Helper()
	var n bundle.Node
	if err := json.Unmarshal([]byte(raw), &n); err != nil {
		t.Fatalf("invalid node JSON: %v", err)
	}
	return n
}

// stubLoopProvider is a mock LLMProvider that returns a fixed structured response.
type stubLoopProvider struct {
	response string
}

func (s *stubLoopProvider) Complete(_ context.Context, req CompletionRequest) (CompletionResponse, error) {
	return CompletionResponse{Content: s.response}, nil
}

// TestLoop_BasicThreeItems checks that a three-item queue produces three results.
func TestLoop_BasicThreeItems(t *testing.T) {
	b, flow := makeLoopBundle(t, `{
		"type": "prompt",
		"inputs": {"item": {"from": "$.item"}},
		"config": {
			"model": "stub/stub",
			"user": "process {{ item }}"
		},
		"output_schema": {
			"type": "object",
			"properties": {"result": {"type": "string"}, "new_items": {"type": "array", "items": {"type": "string"}}}
		}
	}`)

	execCtx := NewExecutionContext(map[string]any{
		"items": []any{"a", "b", "c"},
	})

	provider := &stubLoopProvider{response: `{"result":"ok","new_items":[]}`}

	r := &runner{
		b:        b,
		flow:     flow,
		execCtx:  execCtx,
		provider: provider,
		nextMap:  buildNextMap(flow),
		tracer:   NewTracer(nil, nil),
	}

	loopNode := b.Nodes["looper"]["v1"]
	out, err := r.executeLoop(context.Background(), "looper", loopNode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items, ok := out["items"].([]any)
	if !ok {
		t.Fatalf("expected output.items to be []any, got %T", out["items"])
	}
	if len(items) != 3 {
		t.Errorf("expected 3 results, got %d", len(items))
	}
}

// TestLoop_QueueGrowth verifies that items discovered mid-loop (via append_from)
// are processed before the loop terminates.
func TestLoop_QueueGrowth(t *testing.T) {
	b, flow := makeLoopBundle(t, `{
		"type": "prompt",
		"inputs": {"item": {"from": "$.item"}},
		"config": {"model": "stub/stub", "user": "process {{ item }}"},
		"output_schema": {
			"type": "object",
			"properties": {
				"processed": {"type": "string"},
				"new_items": {"type": "array", "items": {"type": "string"}}
			}
		}
	}`)

	callCount := 0
	provider := &customLoopProvider{fn: func(req CompletionRequest) (CompletionResponse, error) {
		callCount++
		// First call discovers one extra item; subsequent calls discover none.
		if callCount == 1 {
			return CompletionResponse{Content: `{"processed":"a","new_items":["d"]}`}, nil
		}
		return CompletionResponse{Content: fmt.Sprintf(`{"processed":"item%d","new_items":[]}`, callCount)}, nil
	}}

	execCtx := NewExecutionContext(map[string]any{
		"items": []any{"a", "b", "c"},
	})
	r := &runner{
		b:        b,
		flow:     flow,
		execCtx:  execCtx,
		provider: provider,
		nextMap:  buildNextMap(flow),
		tracer:   NewTracer(nil, nil),
	}

	loopNode := b.Nodes["looper"]["v1"]
	out, err := r.executeLoop(context.Background(), "looper", loopNode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := out["items"].([]any)
	// Initial 3 + 1 discovered = 4 total iterations.
	if len(items) != 4 {
		t.Errorf("expected 4 results (3 initial + 1 discovered), got %d", len(items))
	}
	if callCount != 4 {
		t.Errorf("expected 4 LLM calls, got %d", callCount)
	}
}

// TestLoop_CustomAccumulateKey checks that config.accumulate renames the output key.
func TestLoop_CustomAccumulateKey(t *testing.T) {
	b := &bundle.Bundle{
		Manifest: bundle.Manifest{Name: "loop_test", Entry: "main@v1"},
		Nodes: map[string]map[string]bundle.Node{
			"looper": {"v1": {Type: "loop", Config: map[string]json.RawMessage{
				"over":       json.RawMessage(`"$.inputs.items"`),
				"as":         json.RawMessage(`"item"`),
				"do":         json.RawMessage(`"worker"`),
				"accumulate": json.RawMessage(`"verifications"`),
			}}},
			"worker": {"v1": mustUnmarshalNode(t, `{
				"type": "prompt",
				"inputs": {"item": {"from": "$.item"}},
				"config": {"model": "stub/stub", "user": "verify {{ item }}"},
				"output_schema": {"type": "object", "properties": {"ok": {"type": "boolean"}}}
			}`)},
		},
		Flows: map[string]map[string]bundle.Flow{},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}
	flow := bundle.Flow{
		Entry: "looper",
		Nodes: map[string]string{"looper": "looper@v1", "worker": "worker@v1"},
	}

	execCtx := NewExecutionContext(map[string]any{"items": []any{"x", "y"}})
	r := &runner{
		b:        b,
		flow:     flow,
		execCtx:  execCtx,
		provider: &stubLoopProvider{response: `{"ok":true}`},
		nextMap:  buildNextMap(flow),
		tracer:   NewTracer(nil, nil),
	}

	out, err := r.executeLoop(context.Background(), "looper", b.Nodes["looper"]["v1"])
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, hasItems := out["items"]; hasItems {
		t.Error("output should not contain default 'items' key when accumulate is set")
	}
	verifications, ok := out["verifications"].([]any)
	if !ok {
		t.Fatalf("expected 'verifications' key in output, got keys: %v", out)
	}
	if len(verifications) != 2 {
		t.Errorf("expected 2 verifications, got %d", len(verifications))
	}
}

// TestLoop_EmptyQueue returns an empty accumulator without executing the do-node.
func TestLoop_EmptyQueue(t *testing.T) {
	b, flow := makeLoopBundle(t, `{
		"type": "prompt",
		"inputs": {"item": {"from": "$.item"}},
		"config": {"model": "stub/stub", "user": "{{ item }}"},
		"output_schema": {"type": "object", "properties": {"ok": {"type": "boolean"}}}
	}`)

	callCount := 0
	execCtx := NewExecutionContext(map[string]any{"items": []any{}})
	r := &runner{
		b:        b,
		flow:     flow,
		execCtx:  execCtx,
		provider: &customLoopProvider{fn: func(_ CompletionRequest) (CompletionResponse, error) {
			callCount++
			return CompletionResponse{Content: `{"ok":true}`}, nil
		}},
		nextMap: buildNextMap(flow),
		tracer:  NewTracer(nil, nil),
	}

	out, err := r.executeLoop(context.Background(), "looper", b.Nodes["looper"]["v1"])
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	items := out["items"].([]any)
	if len(items) != 0 {
		t.Errorf("expected empty result for empty queue, got %d items", len(items))
	}
	if callCount != 0 {
		t.Errorf("expected 0 LLM calls for empty queue, got %d", callCount)
	}
}

// customLoopProvider lets tests inject per-call logic.
type customLoopProvider struct {
	fn func(CompletionRequest) (CompletionResponse, error)
}

func (p *customLoopProvider) Complete(_ context.Context, req CompletionRequest) (CompletionResponse, error) {
	return p.fn(req)
}
