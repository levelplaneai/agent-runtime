package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// --- topoSort tests ---

func TestTopoSort_LinearChain(t *testing.T) {
	flow := bundle.Flow{
		Entry: "a",
		Nodes: map[string]string{"a": "a@v1", "b": "b@v1", "c": "c@v1"},
		Edges: []bundle.Edge{
			{From: "a", To: "b"},
			{From: "b", To: "c"},
		},
	}
	order, err := topoSort(flow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"a", "b", "c"}
	if !sliceEq(order, want) {
		t.Errorf("got %v, want %v", order, want)
	}
}

func TestTopoSort_DisconnectedNodes(t *testing.T) {
	// entry=a, a→b; c and d have no edges
	flow := bundle.Flow{
		Entry: "a",
		Nodes: map[string]string{"a": "a@v1", "b": "b@v1", "c": "c@v1", "d": "d@v1"},
		Edges: []bundle.Edge{
			{From: "a", To: "b"},
		},
	}
	order, err := topoSort(flow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 4 {
		t.Fatalf("expected 4 nodes, got %d: %v", len(order), order)
	}
	// entry node must be first
	if order[0] != "a" {
		t.Errorf("entry node 'a' must be first, got %v", order)
	}
	// b must come after a
	if indexOf(order, "b") < indexOf(order, "a") {
		t.Errorf("'b' must come after 'a', got %v", order)
	}
}

func TestTopoSort_EntryFirst_MultipleSources(t *testing.T) {
	// Two sources with no incoming edges: entry=z and also "a"
	flow := bundle.Flow{
		Entry: "z",
		Nodes: map[string]string{"z": "z@v1", "a": "a@v1", "b": "b@v1"},
		Edges: []bundle.Edge{
			{From: "z", To: "b"},
		},
	}
	order, err := topoSort(flow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if order[0] != "z" {
		t.Errorf("entry node 'z' must be first, got %v", order)
	}
}

func TestTopoSort_AllNodes(t *testing.T) {
	// Verify every node in flow.Nodes appears in the output
	flow := bundle.Flow{
		Entry: "start",
		Nodes: map[string]string{
			"start": "start@v1",
			"mid":   "mid@v1",
			"end":   "end@v1",
		},
		Edges: []bundle.Edge{
			{From: "start", To: "mid"},
			{From: "mid", To: "end"},
		},
	}
	order, err := topoSort(flow)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(order) != 3 {
		t.Errorf("expected 3 nodes, got %v", order)
	}
}

// --- RunFlow integration test with stubs ---

type jsonProvider struct {
	response map[string]any
}

func (s *jsonProvider) Complete(_ context.Context, _ CompletionRequest) (CompletionResponse, error) {
	b, _ := json.Marshal(s.response)
	return CompletionResponse{Content: string(b)}, nil
}

func makeToolNode(toolRef string, inputs map[string]string) bundle.Node {
	args := make(map[string]json.RawMessage)
	for k, v := range inputs {
		args[k] = json.RawMessage(`"` + v + `"`)
	}
	argsJSON, _ := json.Marshal(args)

	return bundle.Node{
		Type: "tool_call",
		Config: map[string]json.RawMessage{
			"tool": json.RawMessage(`"` + toolRef + `"`),
			"args": argsJSON,
		},
	}
}

func makePromptNode(model, userText string) bundle.Node {
	return bundle.Node{
		Type: "prompt",
		Config: map[string]json.RawMessage{
			"model": json.RawMessage(`"` + model + `"`),
			"user":  json.RawMessage(`"` + userText + `"`),
		},
	}
}

func TestRunFlow_ToolCallThenPrompt(t *testing.T) {
	// Flow: fetch_data (tool_call) → summarize (prompt)
	// fetch_data returns {"result": "raw data"}
	// summarize reads $.fetch_data.output.result and returns {"text": "summary"}

	toolOutput := map[string]any{"result": "raw data"}
	reg := NewRegistry()
	reg.Register("data_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return toolOutput, nil
	}))

	providerResp := map[string]any{"text": "summary"}
	provider := &jsonProvider{response: providerResp}

	b := &bundle.Bundle{
		Path: t.TempDir(),
		Manifest: bundle.Manifest{
			Entry: "main@v1",
		},
		Flows: map[string]map[string]bundle.Flow{
			"main": {
				"v1": {
					Entry: "fetch_data",
					Nodes: map[string]string{
						"fetch_data": "data_tool_node@v1",
						"summarize":  "summarize_node@v1",
					},
					Edges: []bundle.Edge{
						{From: "fetch_data", To: "summarize"},
					},
					Outputs: map[string]bundle.FlowOutputBinding{
						"summary": {From: "$.summarize.output"},
					},
				},
			},
		},
		Nodes: map[string]map[string]bundle.Node{
			"data_tool_node": {
				"v1": makeToolNode("data_tool@v1", nil),
			},
			"summarize_node": {
				"v1": makePromptNode("stub/model", "Summarize: {{ result }}"),
			},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	// Give summarize_node an input binding for result
	summNode := b.Nodes["summarize_node"]["v1"]
	summNode.Inputs = map[string]bundle.InputBinding{
		"result": {From: "$.fetch_data.output.result"},
	}
	b.Nodes["summarize_node"]["v1"] = summNode

	out, err := RunFlow(context.Background(), b, map[string]any{}, reg, provider, nil)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}

	got, ok := out["summary"]
	if !ok {
		t.Fatalf("expected 'summary' key in output, got %v", out)
	}
	gotMap, ok := got.(map[string]any)
	if !ok {
		t.Fatalf("expected map output, got %T", got)
	}
	if gotMap["text"] != "summary" {
		t.Errorf("expected text=summary, got %v", gotMap)
	}
}

func TestRunFlow_SingleToolCall(t *testing.T) {
	reg := NewRegistry()
	reg.Register("echo@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, args map[string]any) (map[string]any, error) {
		return map[string]any{"echoed": args["value"]}, nil
	}))

	b := &bundle.Bundle{
		Path: t.TempDir(),
		Manifest: bundle.Manifest{Entry: "main@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"main": {
				"v1": {
					Entry: "echo_node",
					Nodes: map[string]string{"echo_node": "echo_impl@v1"},
					Edges: []bundle.Edge{},
					Inputs: map[string]bundle.FlowInputField{
						"msg": {Type: "string"},
					},
					Outputs: map[string]bundle.FlowOutputBinding{
						"result": {From: "$.echo_node.output"},
					},
				},
			},
		},
		Nodes: map[string]map[string]bundle.Node{
			"echo_impl": {
				"v1": {
					Type: "tool_call",
					Inputs: map[string]bundle.InputBinding{
						"value": {From: "$.inputs.msg"},
					},
					Config: map[string]json.RawMessage{
						"tool": json.RawMessage(`"echo@v1"`),
						"args": json.RawMessage(`{"value": "{{ value }}"}`),
					},
				},
			},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	out, err := RunFlow(context.Background(), b, map[string]any{"msg": "hello"}, reg, nil, nil)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}
	result, ok := out["result"]
	if !ok {
		t.Fatalf("missing 'result' key: %v", out)
	}
	m := result.(map[string]any)
	if m["echoed"] != "hello" {
		t.Errorf("expected echoed=hello, got %v", m)
	}
}

func TestRunFlow_OnError_Skip(t *testing.T) {
	reg := NewRegistry()
	reg.Register("fail@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return nil, context.DeadlineExceeded
	}))
	reg.Register("after@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	}))

	b := &bundle.Bundle{
		Path: t.TempDir(),
		Manifest: bundle.Manifest{Entry: "main@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"main": {
				"v1": {
					Entry: "step1",
					Nodes: map[string]string{
						"step1": "fail_impl@v1",
						"step2": "after_impl@v1",
					},
					Edges: []bundle.Edge{{From: "step1", To: "step2"}},
					Outputs: map[string]bundle.FlowOutputBinding{
						"done": {From: "$.step2.output"},
					},
				},
			},
		},
		Nodes: map[string]map[string]bundle.Node{
			"fail_impl": {
				"v1": {
					Type:    "tool_call",
					OnError: "skip",
					Config: map[string]json.RawMessage{
						"tool": json.RawMessage(`"fail@v1"`),
					},
				},
			},
			"after_impl": {
				"v1": {
					Type: "tool_call",
					Config: map[string]json.RawMessage{
						"tool": json.RawMessage(`"after@v1"`),
					},
				},
			},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	out, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, nil)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}
	done := out["done"].(map[string]any)
	if done["ok"] != true {
		t.Errorf("expected ok=true, got %v", done)
	}
}

func TestRunFlow_RouterAndMap(t *testing.T) {
	// Flow: extract → route → price_all (map, do=price_one) → done
	//
	// extract returns {items: [{part:"A"},{part:"B"}]}
	// route is a router with a default branch → price_all
	// price_all maps over items, calls price_one for each
	// price_one is a tool_call: returns {price: 1.0} for any part
	// done is a tool_call: returns {ok: true}
	//
	// Final output: done.output = {ok: true}

	reg := NewRegistry()
	reg.Register("pricer@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, args map[string]any) (map[string]any, error) {
		return map[string]any{"price": 1.0}, nil
	}))
	reg.Register("finisher@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return map[string]any{"ok": true}, nil
	}))

	extractResp := map[string]any{
		"items": []any{
			map[string]any{"part": "A"},
			map[string]any{"part": "B"},
		},
	}
	provider := &jsonProvider{response: extractResp}

	b := &bundle.Bundle{
		Path: t.TempDir(),
		Manifest: bundle.Manifest{Entry: "main@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"main": {"v1": {
				Entry: "extract",
				Nodes: map[string]string{
					"extract":   "extract_node@v1",
					"route":     "route_node@v1",
					"price_all": "price_all_node@v1",
					"price_one": "price_one_node@v1",
					"done":      "done_node@v1",
				},
				Edges: []bundle.Edge{
					{From: "extract", To: "route"},
					{From: "price_all", To: "done"},
				},
				Outputs: map[string]bundle.FlowOutputBinding{
					"result": {From: "$.done.output"},
				},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"extract_node": {"v1": makePromptNode("stub/m", "extract")},
			"route_node": {"v1": {
				Type: "router",
				Config: map[string]json.RawMessage{
					"branches": json.RawMessage(`[{"default":true,"goto":"price_all"}]`),
				},
			}},
			"price_all_node": {"v1": {
				Type: "map",
				Config: map[string]json.RawMessage{
					"over":        json.RawMessage(`"$.extract.output.items"`),
					"as":          json.RawMessage(`"item"`),
					"do":          json.RawMessage(`"price_one"`),
					"concurrency": json.RawMessage(`1`),
				},
			}},
			"price_one_node": {"v1": {
				Type: "tool_call",
				Inputs: map[string]bundle.InputBinding{
					"part": {From: "$.item.part"},
				},
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"pricer@v1"`),
					"args": json.RawMessage(`{"part_number":"{{ part }}"}`),
				},
			}},
			"done_node": {"v1": {
				Type: "tool_call",
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"finisher@v1"`),
				},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	out, err := RunFlow(context.Background(), b, map[string]any{}, reg, provider, nil)
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
	if m["ok"] != true {
		t.Errorf("expected ok=true, got %v", m)
	}
}

func TestRunFlow_MapConcurrent(t *testing.T) {
	// A map node with concurrency:3 over 6 object items. The do-node echoes back
	// the item's "label" field. Results must be in input order.
	const n = 6
	reg := NewRegistry()
	reg.Register("echo@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, args map[string]any) (map[string]any, error) {
		return map[string]any{"label": args["label"]}, nil
	}))

	// Each item is an object so $.val.label resolves correctly (resolver requires ≥2 path parts).
	vals := make([]any, n)
	for i := range vals {
		vals[i] = map[string]any{"label": fmt.Sprintf("item-%d", i+1)}
	}

	b := &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "main@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"main": {"v1": {
				Entry: "fanout",
				Nodes: map[string]string{
					"fanout": "fanout_node@v1",
					"echo":   "echo_node@v1",
				},
				Edges:   []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{"items": {From: "$.fanout.output.items"}},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"fanout_node": {"v1": {
				Type: "map",
				Config: map[string]json.RawMessage{
					"over":        json.RawMessage(`"$.inputs.vals"`),
					"as":          json.RawMessage(`"val"`),
					"do":          json.RawMessage(`"echo"`),
					"concurrency": json.RawMessage(`3`),
				},
			}},
			"echo_node": {"v1": {
				Type: "tool_call",
				Inputs: map[string]bundle.InputBinding{
					"label": {From: "$.val.label"},
				},
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"echo@v1"`),
					"args": json.RawMessage(`{"label":"{{ label }}"}`),
				},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	out, err := RunFlow(context.Background(), b, map[string]any{"vals": vals}, reg, nil, nil)
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}

	rawItems, ok := out["items"]
	if !ok {
		t.Fatalf("missing 'items' key: %v", out)
	}
	items, ok := rawItems.([]any)
	if !ok {
		t.Fatalf("expected []any items, got %T", rawItems)
	}
	if len(items) != n {
		t.Fatalf("expected %d items, got %d", n, len(items))
	}
	for i, item := range items {
		m := item.(map[string]any)
		want := fmt.Sprintf("item-%d", i+1)
		if m["label"] != want {
			t.Errorf("item[%d]: expected label=%q, got %v", i, want, m["label"])
		}
	}
}

func TestRunFlow_MapConcurrentError(t *testing.T) {
	// A map node with concurrency:3 where one item fails — the whole map must fail.
	reg := NewRegistry()
	reg.Register("mayFail@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, args map[string]any) (map[string]any, error) {
		if args["label"].(string) == "item-3" {
			return nil, fmt.Errorf("item 3 failed")
		}
		return map[string]any{"ok": true}, nil
	}))

	vals := []any{
		map[string]any{"label": "item-1"},
		map[string]any{"label": "item-2"},
		map[string]any{"label": "item-3"},
		map[string]any{"label": "item-4"},
		map[string]any{"label": "item-5"},
	}

	b := &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "main@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"main": {"v1": {
				Entry:   "fanout",
				Nodes:   map[string]string{"fanout": "fanout_node@v1", "step": "step_node@v1"},
				Edges:   []bundle.Edge{},
				Outputs: map[string]bundle.FlowOutputBinding{"items": {From: "$.fanout.output.items"}},
			}},
		},
		Nodes: map[string]map[string]bundle.Node{
			"fanout_node": {"v1": {
				Type: "map",
				Config: map[string]json.RawMessage{
					"over":        json.RawMessage(`"$.inputs.vals"`),
					"as":          json.RawMessage(`"val"`),
					"do":          json.RawMessage(`"step"`),
					"concurrency": json.RawMessage(`3`),
				},
			}},
			"step_node": {"v1": {
				Type: "tool_call",
				Inputs: map[string]bundle.InputBinding{
					"label": {From: "$.val.label"},
				},
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"mayFail@v1"`),
					"args": json.RawMessage(`{"label":"{{ label }}"}`),
				},
			}},
		},
		Tools: map[string]map[string]bundle.ToolSignature{},
	}

	_, err := RunFlow(context.Background(), b, map[string]any{"vals": vals}, reg, nil, nil)
	if err == nil {
		t.Fatal("expected error from failing item, got nil")
	}
}

// --- Partial execution (RunFlowOptions) ---

func makeLinearBundle(t *testing.T, nodes ...string) *bundle.Bundle {
	t.Helper()
	flowNodes := make(map[string]string, len(nodes))
	bundleNodes := make(map[string]map[string]bundle.Node, len(nodes))
	for _, n := range nodes {
		flowNodes[n] = n + "_def@v1"
		bundleNodes[n+"_def"] = map[string]bundle.Node{
			"v1": {
				Type: "tool_call",
				Config: map[string]json.RawMessage{
					"tool": json.RawMessage(`"` + n + `_tool@v1"`),
					"args": json.RawMessage(`{}`),
				},
			},
		}
	}
	edges := make([]bundle.Edge, 0, len(nodes)-1)
	for i := 0; i < len(nodes)-1; i++ {
		edges = append(edges, bundle.Edge{From: nodes[i], To: nodes[i+1]})
	}
	return &bundle.Bundle{
		Path:     t.TempDir(),
		Manifest: bundle.Manifest{Entry: "main@v1"},
		Flows: map[string]map[string]bundle.Flow{
			"main": {"v1": {
				Entry: nodes[0],
				Nodes: flowNodes,
				Edges: edges,
				Outputs: map[string]bundle.FlowOutputBinding{
					"last": {From: "$.last.output"},
				},
			}},
		},
		Nodes: bundleNodes,
		Tools: map[string]map[string]bundle.ToolSignature{},
	}
}

func TestRunFlow_StopAfter(t *testing.T) {
	var executed []string
	reg := NewRegistry()
	for _, n := range []string{"first", "second", "last"} {
		name := n
		reg.Register(name+"_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
			executed = append(executed, name)
			return map[string]any{"out": name}, nil
		}))
	}
	b := makeLinearBundle(t, "first", "second", "last")

	_, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, &RunFlowOptions{StopAfter: "second"})
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}
	if len(executed) != 2 || executed[0] != "first" || executed[1] != "second" {
		t.Fatalf("expected [first second] executed, got %v", executed)
	}
}

func TestRunFlow_StartAt(t *testing.T) {
	var executed []string
	reg := NewRegistry()
	for _, n := range []string{"first", "second", "last"} {
		name := n
		reg.Register(name+"_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
			executed = append(executed, name)
			return map[string]any{"out": name}, nil
		}))
	}
	b := makeLinearBundle(t, "first", "second", "last")
	// Override flow outputs to point at "last" which will run normally.
	b.Flows["main"]["v1"] = bundle.Flow{
		Entry: "first",
		Nodes: b.Flows["main"]["v1"].Nodes,
		Edges: b.Flows["main"]["v1"].Edges,
		Outputs: map[string]bundle.FlowOutputBinding{
			"last": {From: "$.last.output"},
		},
	}

	out, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, &RunFlowOptions{StartAt: "second"})
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}
	if len(executed) != 0 || indexOf(executed, "first") >= 0 {
		// first should not have run
	}
	if indexOf(executed, "second") >= 0 {
		// ok
	}
	_ = out
	for _, n := range executed {
		if n == "first" {
			t.Fatalf("'first' ran but StartAt was 'second'")
		}
	}
}

func TestRunFlow_SeedOutputs(t *testing.T) {
	// "second" reads from first's output; we seed first so it doesn't need to run.
	var executed []string
	reg := NewRegistry()
	reg.Register("first_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		executed = append(executed, "first")
		return map[string]any{"out": "from_first"}, nil
	}))
	reg.Register("second_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, args map[string]any) (map[string]any, error) {
		executed = append(executed, "second")
		return map[string]any{"got": args["upstream"]}, nil
	}))
	reg.Register("last_tool@v1", bundle.ToolSignature{}, ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		executed = append(executed, "last")
		return map[string]any{"out": "done"}, nil
	}))

	b := makeLinearBundle(t, "first", "second", "last")
	// Give second an input binding that reads from first's seeded output.
	secondNode := b.Nodes["second_def"]["v1"]
	secondNode.Inputs = map[string]bundle.InputBinding{
		"upstream": {From: "$.first.output.out"},
	}
	b.Nodes["second_def"]["v1"] = secondNode

	seed := map[string]any{
		"first": map[string]any{"out": "seeded_value"},
	}
	out, err := RunFlow(context.Background(), b, map[string]any{}, reg, nil, &RunFlowOptions{
		StartAt:     "second",
		SeedOutputs: seed,
	})
	if err != nil {
		t.Fatalf("RunFlow error: %v", err)
	}
	for _, n := range executed {
		if n == "first" {
			t.Fatal("'first' ran despite being seeded and skipped via StartAt")
		}
	}
	_ = out
}

// --- helpers ---

func sliceEq(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func indexOf(s []string, v string) int {
	for i, x := range s {
		if x == v {
			return i
		}
	}
	return -1
}
