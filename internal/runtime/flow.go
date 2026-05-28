package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// RunFlowOptions configures optional partial-execution behaviour for RunFlow.
// All fields are optional; zero values restore default full-flow behaviour.
type RunFlowOptions struct {
	StartAt     string         // local node name to begin from (empty means flow.Entry)
	StopAfter   string         // local node name to halt after (empty means run to completion)
	SeedOutputs map[string]any // pre-populate node outputs before execution starts
}

// errStopAfterReached is a sentinel returned by runFrontier when execution halts at
// the node specified by RunFlowOptions.StopAfter. RunFlow converts it to a clean result.
var errStopAfterReached = errors.New("stop after reached")

// RunFlow executes the bundle's entry flow with the given inputs.
//
// It resolves the entry flow from the manifest, then runs nodes via a
// frontier-based loop: starting from the entry node, each completed node
// determines what runs next based on its type and output. Router nodes
// choose a single branch; map nodes fan out internally. Static edges in
// flow.Edges advance linear chains.
//
// reg provides callable tools; provider is used for prompt nodes. Either may be
// nil if the flow contains no nodes of the corresponding type.
func RunFlow(
	ctx context.Context,
	b *bundle.Bundle,
	inputs map[string]any,
	reg *Registry,
	provider LLMProvider,
	opts *RunFlowOptions,
) (map[string]any, error) {
	flowName, flowVersion, ok := bundle.ParseRef(b.Manifest.Entry)
	if !ok {
		return nil, fmt.Errorf("manifest.entry %q: invalid name@version format", b.Manifest.Entry)
	}
	versions, ok := b.Flows[flowName]
	if !ok {
		return nil, fmt.Errorf("flow %q not found in bundle", flowName)
	}
	flow, ok := versions[flowVersion]
	if !ok {
		return nil, fmt.Errorf("flow %q version %q not found in bundle", flowName, flowVersion)
	}

	t := tracerFrom(ctx)
	flowStart := time.Now()
	t.Emit(TraceEvent{
		Event:  "flow_start",
		Bundle: b.Manifest.Name,
		Flow:   b.Manifest.Entry,
		Inputs: inputs,
	})

	execCtx := NewExecutionContext(inputs)
	if opts != nil {
		for k, v := range opts.SeedOutputs {
			execCtx.SetNodeOutput(k, v)
		}
	}
	r := &runner{
		b:        b,
		flow:     flow,
		execCtx:  execCtx,
		reg:      reg,
		provider: provider,
		nextMap:  buildNextMap(flow),
		tracer:   t,
	}

	if err := r.runFrontier(ctx, opts); err != nil {
		if errors.Is(err, errStopAfterReached) {
			t.Emit(TraceEvent{
				Event:      "flow_done",
				Bundle:     b.Manifest.Name,
				Flow:       b.Manifest.Entry,
				DurationMS: time.Since(flowStart).Milliseconds(),
			})
			return execCtx.AllNodeOutputs(), nil
		}
		t.Emit(TraceEvent{
			Event:      "flow_error",
			Bundle:     b.Manifest.Name,
			Error:      err.Error(),
			DurationMS: time.Since(flowStart).Milliseconds(),
		})
		return nil, err
	}

	result, err := resolveFlowOutputs(flow, execCtx)
	if err != nil {
		t.Emit(TraceEvent{
			Event:      "flow_error",
			Bundle:     b.Manifest.Name,
			Error:      err.Error(),
			DurationMS: time.Since(flowStart).Milliseconds(),
		})
		return nil, err
	}
	t.Emit(TraceEvent{
		Event:      "flow_done",
		Bundle:     b.Manifest.Name,
		Flow:       b.Manifest.Entry,
		DurationMS: time.Since(flowStart).Milliseconds(),
	})
	return result, nil
}

// runner holds shared state for one flow execution. Its executeNode method is
// the single dispatch point used by both the frontier loop and map iterations,
// ensuring one switch, one trace path, and no code duplication.
type runner struct {
	b        *bundle.Bundle
	flow     bundle.Flow
	execCtx  *ExecutionContext
	reg      *Registry
	provider LLMProvider
	// nextMap maps each node to its successor via the static edge graph.
	// The spec allows at most one outgoing edge per node (fan-out belongs in
	// parallel nodes); the validator does not currently enforce this, so a
	// second edge from the same source would silently overwrite the first.
	nextMap map[string]string
	tracer  *Tracer
	depth   int // subflow nesting depth; capped at maxSubflowDepth
}

const maxSubflowDepth = 32

// executeNode resolves, traces, dispatches, and stores the output for one node.
// For router nodes it also returns the chosen gotoTarget; all others return "".
func (r *runner) executeNode(ctx context.Context, localName string) (map[string]any, string, error) {
	nodeRef, ok := r.flow.Nodes[localName]
	if !ok {
		return nil, "", fmt.Errorf("node %q not found in flow", localName)
	}
	nodeName, nodeVersion, ok := bundle.ParseRef(nodeRef)
	if !ok {
		return nil, "", fmt.Errorf("node %q: invalid ref %q", localName, nodeRef)
	}
	node, ok := r.b.Nodes[nodeName][nodeVersion]
	if !ok {
		return nil, "", fmt.Errorf("node %q: %q not found in bundle", localName, nodeRef)
	}
	nodeDir := filepath.Join(r.b.Path, "nodes", nodeName, nodeVersion)

	r.execCtx.SetCurrentNode(localName)
	nodeStart := time.Now()
	r.tracer.Emit(TraceEvent{Event: "node_start", Node: localName, NodeType: node.Type})

	output, err := ApplyErrorPolicy(ctx, localName, node, func() (map[string]any, error) {
		switch node.Type {
		case "tool_call":
			return ExecuteToolCall(ctx, node, r.execCtx, r.reg)
		case "prompt":
			return ExecutePrompt(ctx, node, nodeDir, r.execCtx, r.provider, r.reg)
		case "router":
			return ExecuteRouter(ctx, localName, node, nodeDir, r.execCtx, r.provider)
		case "map":
			return r.executeMap(ctx, localName, node)
		case "parallel":
			return r.executeParallel(ctx, localName, node)
		case "subflow":
			return r.executeSubflow(ctx, localName, node)
		default:
			return nil, fmt.Errorf("unknown node type %q", node.Type)
		}
	})
	if err != nil {
		r.tracer.Emit(TraceEvent{Event: "node_error", Node: localName, Error: err.Error()})
		return nil, "", err
	}

	// Extract the router's goto target and strip the internal key so it is
	// never stored in execCtx or surfaced to callers as node output.
	gotoTarget := ""
	if node.Type == "router" {
		if gt, ok := output["_goto"].(string); ok {
			gotoTarget = gt
			delete(output, "_goto")
		}
	}

	traceOutput := output
	if node.Type == "tool_call" {
		traceOutput = sanitizeOutputForTrace(output)
	}
	r.tracer.Emit(TraceEvent{
		Event:      "node_done",
		Node:       localName,
		NodeType:   node.Type,
		Output:     traceOutput,
		DurationMS: time.Since(nodeStart).Milliseconds(),
	})
	r.execCtx.SetNodeOutput(localName, output)

	return output, gotoTarget, nil
}

// runFrontier drives the frontier-based execution loop for r.flow. opts may be
// nil (full run from flow.Entry). When opts.StopAfter is reached it returns
// errStopAfterReached so the caller can surface a partial result.
func (r *runner) runFrontier(ctx context.Context, opts *RunFlowOptions) error {
	start := r.flow.Entry
	if opts != nil && opts.StartAt != "" {
		start = opts.StartAt
	}
	frontier := []string{start}
	visited := make(map[string]bool)
	for len(frontier) > 0 {
		localName := frontier[0]
		frontier = frontier[1:]
		if visited[localName] {
			continue
		}
		visited[localName] = true
		_, gotoTarget, err := r.executeNode(ctx, localName)
		if err != nil {
			return fmt.Errorf("node %q: %w", localName, err)
		}
		if opts != nil && opts.StopAfter != "" && localName == opts.StopAfter {
			return errStopAfterReached
		}
		if gotoTarget != "" {
			frontier = append(frontier, gotoTarget)
		} else if next, ok := r.nextMap[localName]; ok {
			frontier = append(frontier, next)
		}
	}
	return nil
}

// buildNextMap builds a from→to lookup from the flow's static edges.
func buildNextMap(flow bundle.Flow) map[string]string {
	next := make(map[string]string, len(flow.Edges))
	for _, e := range flow.Edges {
		next[e.From] = e.To
	}
	return next
}

// topoSort returns a deterministic topological execution order for all nodes in
// the flow. Kahn's algorithm is used; among nodes with equal in-degree the flow
// entry node is scheduled first, then remaining ties are broken alphabetically.
//
// No longer called by RunFlow (which uses frontier-based execution), but kept
// for the existing TestTopoSort_* tests.
func topoSort(flow bundle.Flow) ([]string, error) {
	inDegree := make(map[string]int, len(flow.Nodes))
	adj := make(map[string][]string, len(flow.Nodes))

	for localName := range flow.Nodes {
		inDegree[localName] = 0
	}
	for _, edge := range flow.Edges {
		adj[edge.From] = append(adj[edge.From], edge.To)
		inDegree[edge.To]++
	}

	var ready []string
	for localName := range flow.Nodes {
		if inDegree[localName] == 0 {
			ready = append(ready, localName)
		}
	}
	sortNodes(ready, flow.Entry)

	order := make([]string, 0, len(flow.Nodes))
	for len(ready) > 0 {
		node := ready[0]
		ready = ready[1:]
		order = append(order, node)

		for _, succ := range adj[node] {
			inDegree[succ]--
			if inDegree[succ] == 0 {
				ready = append(ready, succ)
				sortNodes(ready, flow.Entry)
			}
		}
	}

	if len(order) != len(flow.Nodes) {
		return nil, fmt.Errorf("cycle detected in flow graph")
	}
	return order, nil
}

// sortNodes sorts node names in-place: entry node first, then alphabetically.
func sortNodes(nodes []string, entry string) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i] == entry {
			return true
		}
		if nodes[j] == entry {
			return false
		}
		return nodes[i] < nodes[j]
	})
}

// resolveFlowOutputs evaluates each flow output binding against the execution
// context and returns the resolved key→value map.
func resolveFlowOutputs(flow bundle.Flow, execCtx *ExecutionContext) (map[string]any, error) {
	result := make(map[string]any, len(flow.Outputs))
	for name, binding := range flow.Outputs {
		val, err := Resolve(execCtx, binding.From)
		if err != nil {
			return nil, fmt.Errorf("flow output %q: %w", name, err)
		}
		result[name] = val
	}
	return result, nil
}
