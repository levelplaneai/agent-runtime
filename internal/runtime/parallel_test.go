package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

func TestRunFlow_ParallelBasic(t *testing.T) {
	// Flow: par (parallel, branches={alpha:do_alpha, beta:do_beta})
	// do_alpha returns {"name":"alpha"}, do_beta returns {"name":"beta"}
	// Output: $.par.output = {"alpha":{"name":"alpha"}, "beta":{"name":"beta"}}
	reg := NewRegistry()
	reg.Register("alpha_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"name": "alpha"}, nil
	}))
	reg.Register("beta_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"name": "beta"}, nil
	}))

	b := &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "main@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"main": {"v1": {
				Entry: "par",
				Nodes: map[string]string{
					"par":      "par_node@v1",
					"do_alpha": "alpha_node@v1",
					"do_beta":  "beta_node@v1",
				},
				Edges:   []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{"result": {From: "$.par.output"}},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"par_node": {"v1": {
				Type: "parallel",
				Config: map[string]json.RawMessage{
					"branches": json.RawMessage(`{"alpha":"do_alpha","beta":"do_beta"}`),
				},
			}},
			"alpha_node": {"v1": {
				Type: "tool_call",
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"alpha_tool@v1"`),
				},
			}},
			"beta_node": {"v1": {
				Type: "tool_call",
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"beta_tool@v1"`),
				},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	out, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, nil)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}

	result, ok := out["result"]
	if !ok {
		t.Fatalf("missing 'result' key: %v", out)
	}
	m, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("expected map result, got %T", result)
	}

	for _, branch := range []string{"alpha", "beta"} {
		v, ok := m[branch]
		if !ok {
			t.Errorf("missing branch %q in result: %v", branch, m)
			continue
		}
		bm, ok := v.(map[string]any)
		if !ok {
			t.Errorf("branch %q: expected map, got %T", branch, v)
			continue
		}
		if bm["name"] != branch {
			t.Errorf("branch %q: expected name=%q, got %v", branch, branch, bm["name"])
		}
	}
}

func TestRunFlow_ParallelError(t *testing.T) {
	// One branch fails — RunFlow must return an error.
	reg := NewRegistry()
	reg.Register("ok_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	}))
	reg.Register("bad_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return nil, fmt.Errorf("branch exploded")
	}))

	b := &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "main@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"main": {"v1": {
				Entry: "par",
				Nodes: map[string]string{
					"par":    "par_node@v1",
					"do_ok":  "ok_node@v1",
					"do_bad": "bad_node@v1",
				},
				Edges:   []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{"result": {From: "$.par.output"}},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"par_node": {"v1": {
				Type: "parallel",
				Config: map[string]json.RawMessage{
					"branches": json.RawMessage(`{"ok":"do_ok","bad":"do_bad"}`),
				},
			}},
			"ok_node": {"v1": {
				Type: "tool_call",
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"ok_tool@v1"`),
				},
			}},
			"bad_node": {"v1": {
				Type: "tool_call",
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"bad_tool@v1"`),
				},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	_, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, nil)
	if err == nil {
		t.Fatal("expected error from failing branch, got nil")
	}
}

func TestRunFlow_ParallelBranchIsolation(t *testing.T) {
	// A node runs before the parallel, producing output. Both parallel branches
	// read from that prior output via input bindings, verifying that each branch's
	// cloned context carries parent state. Branches cannot read each other's outputs.
	reg := NewRegistry()
	reg.Register("setup_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"value": "shared"}, nil
	}))
	reg.Register("echo_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, args map[string]any) (map[string]any, error) {
		return map[string]any{"echoed": args["v"]}, nil
	}))

	b := &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "main@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"main": {"v1": {
				Entry: "setup",
				Nodes: map[string]string{
					"setup":  "setup_node@v1",
					"par":    "par_node@v1",
					"do_a":   "echo_a_node@v1",
					"do_b":   "echo_b_node@v1",
				},
				Edges:   []bundle.Edge{{From: "setup", To: "par"}},
				Outputs: map[string]bundle.FlowOutputBinding{"result": {From: "$.par.output"}},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"setup_node": {"v1": {
				Type: "tool_call",
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"setup_tool@v1"`),
				},
			}},
			"par_node": {"v1": {
				Type: "parallel",
				Config: map[string]json.RawMessage{
					"branches": json.RawMessage(`{"a":"do_a","b":"do_b"}`),
				},
			}},
			"echo_a_node": {"v1": {
				Type: "tool_call",
				Inputs: map[string]bundle.InputBinding{
					"v": {From: "$.setup.output.value"},
				},
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"echo_tool@v1"`),
					"args": json.RawMessage(`{"v":"{{ v }}"}`),
				},
			}},
			"echo_b_node": {"v1": {
				Type: "tool_call",
				Inputs: map[string]bundle.InputBinding{
					"v": {From: "$.setup.output.value"},
				},
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"echo_tool@v1"`),
					"args": json.RawMessage(`{"v":"{{ v }}"}`),
				},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	out, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, nil)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}

	result := out["result"].(map[string]any)
	for _, branch := range []string{"a", "b"} {
		bm, ok := result[branch].(map[string]any)
		if !ok {
			t.Fatalf("branch %q: expected map result", branch)
		}
		if bm["echoed"] != "shared" {
			t.Errorf("branch %q: expected echoed=shared, got %v", branch, bm["echoed"])
		}
	}
}
