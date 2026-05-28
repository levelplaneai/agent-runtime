package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// echoTool returns its "value" arg as {"result": value}.
func makeEchoReg(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry()
	reg.Register("echo@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, args map[string]any) (map[string]any, error) {
		return map[string]any{"result": args["value"]}, nil
	}))
	return reg
}

func TestRunFlow_SubflowBasic(t *testing.T) {
	// Parent flow: subflow node calls "child@v1" which echoes a hardcoded value.
	// Parent output: $.sub.output.result == "hello"
	reg := makeEchoReg(t)

	b := &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "parent@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"parent": {"v1": {
				Entry: "sub",
				Nodes: map[string]string{"sub": "subflow_node@v1"},
				Edges: []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{
					"result": {From: "$.sub.output.result"},
				},
			}},
			"child": {"v1": {
				Entry: "echo",
				Nodes: map[string]string{"echo": "echo_node@v1"},
				Edges: []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{
					"result": {From: "$.echo.output.result"},
				},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"subflow_node": {"v1": {
				Type:   "subflow",
				Inputs: map[string]bundle.InputBinding{},
				Config: map[string]json.RawMessage{
					"flow": json.RawMessage(`"child@v1"`),
				},
			}},
			"echo_node": {"v1": {
				Type:   "tool_call",
				Inputs: map[string]bundle.InputBinding{},
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"echo@v1"`),
					"args": json.RawMessage(`{"value":"hello"}`),
				},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	out, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, nil)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}
	if out["result"] != "hello" {
		t.Errorf("expected result=hello, got %v", out["result"])
	}
}

func TestRunFlow_SubflowInputMapping(t *testing.T) {
	// Parent flow: first node produces {"msg": "world"}.
	// Subflow node maps that output into the child flow's "value" input.
	// Child flow echoes it. Parent reads the result.
	reg := NewRegistry()
	reg.Register("producer@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"msg": "world"}, nil
	}))
	reg.Register("echo@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, args map[string]any) (map[string]any, error) {
		return map[string]any{"result": args["value"]}, nil
	}))

	b := &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "parent@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"parent": {"v1": {
				Entry: "produce",
				Nodes: map[string]string{
					"produce": "producer_node@v1",
					"sub":     "subflow_node@v1",
				},
				Edges: []bundle.Edge{{From: "produce", To: "sub"}},
				Outputs: map[string]bundle.FlowOutputBinding{
					"result": {From: "$.sub.output.result"},
				},
			}},
			"child": {"v1": {
				Entry: "echo",
				Nodes: map[string]string{"echo": "echo_node@v1"},
				Edges: []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{
					"result": {From: "$.echo.output.result"},
				},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"producer_node": {"v1": {
				Type:   "tool_call",
				Inputs: map[string]bundle.InputBinding{},
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"producer@v1"`),
				},
			}},
			"subflow_node": {"v1": {
				Type: "subflow",
				Inputs: map[string]bundle.InputBinding{
					"value": {From: "$.produce.output.msg"},
				},
				Config: map[string]json.RawMessage{
					"flow": json.RawMessage(`"child@v1"`),
				},
			}},
			"echo_node": {"v1": {
				Type: "tool_call",
				Inputs: map[string]bundle.InputBinding{
					"value": {From: "$.inputs.value"},
				},
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"echo@v1"`),
					"args": json.RawMessage(`{"value":"{{ value }}"}`),
				},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	out, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, nil)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}
	if out["result"] != "world" {
		t.Errorf("expected result=world, got %v", out["result"])
	}
}

func TestRunFlow_SubflowError(t *testing.T) {
	// Child flow node fails — error must propagate to the parent RunFlow call.
	reg := NewRegistry()
	reg.Register("bad@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return nil, fmt.Errorf("child exploded")
	}))

	b := &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "parent@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"parent": {"v1": {
				Entry:   "sub",
				Nodes:   map[string]string{"sub": "subflow_node@v1"},
				Edges:   []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{},
			}},
			"child": {"v1": {
				Entry:   "bad",
				Nodes:   map[string]string{"bad": "bad_node@v1"},
				Edges:   []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"subflow_node": {"v1": {
				Type:   "subflow",
				Inputs: map[string]bundle.InputBinding{},
				Config: map[string]json.RawMessage{
					"flow": json.RawMessage(`"child@v1"`),
				},
			}},
			"bad_node": {"v1": {
				Type:   "tool_call",
				Inputs: map[string]bundle.InputBinding{},
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"bad@v1"`),
				},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	_, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, nil)
	if err == nil {
		t.Fatal("expected error from failing child, got nil")
	}
}

func TestRunFlow_SubflowMissingFlowRef(t *testing.T) {
	// Subflow node with no config.flow — must return a descriptive error.
	b := &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "parent@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"parent": {"v1": {
				Entry:   "sub",
				Nodes:   map[string]string{"sub": "subflow_node@v1"},
				Edges:   []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"subflow_node": {"v1": {
				Type:   "subflow",
				Inputs: map[string]bundle.InputBinding{},
				Config: map[string]json.RawMessage{},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	_, err := RunFlow(context.Background(), b, map[string]any{}, NewRegistry(), nil, nil)
	if err == nil {
		t.Fatal("expected error for missing config.flow, got nil")
	}
}

func TestRunFlow_SubflowDepthLimit(t *testing.T) {
	// A subflow that calls itself via a self-referencing flow.
	// The depth limit must kick in before the stack overflows.
	reg := NewRegistry()

	// Build a bundle where "self@v1" is a flow with one subflow node that calls "self@v1".
	b := &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "self@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"self": {"v1": {
				Entry:   "loop",
				Nodes:   map[string]string{"loop": "loop_node@v1"},
				Edges:   []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"loop_node": {"v1": {
				Type:   "subflow",
				Inputs: map[string]bundle.InputBinding{},
				Config: map[string]json.RawMessage{
					"flow": json.RawMessage(`"self@v1"`),
				},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	_, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, nil)
	if err == nil {
		t.Fatal("expected depth-limit error for recursive subflow, got nil")
	}
}
