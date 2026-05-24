package runtime

import (
	"context"
	"fmt"
	"time"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// executeSubflow runs a subflow node: resolves the target flow from config.flow,
// maps this node's inputs into the child flow's inputs, executes the child flow
// via a fresh runner, and returns the child flow's declared outputs.
//
// The parent runner's registry, provider, tracer, and bundle are shared with
// the child runner. The child gets its own ExecutionContext so its node outputs
// are isolated from the parent.
func (r *runner) executeSubflow(ctx context.Context, localName string, node bundle.Node) (map[string]any, error) {
	if r.depth+1 > maxSubflowDepth {
		return nil, fmt.Errorf("subflow node: maximum nesting depth %d exceeded", maxSubflowDepth)
	}

	flowRef, err := configString(node.Config, "flow")
	if err != nil {
		return nil, fmt.Errorf("subflow node: %w", err)
	}

	flowName, flowVersion, ok := bundle.ParseRef(flowRef)
	if !ok {
		return nil, fmt.Errorf("subflow node: config.flow %q: must use name@version format", flowRef)
	}
	versions, ok := r.b.Flows[flowName]
	if !ok {
		return nil, fmt.Errorf("subflow node: flow %q not found in bundle", flowRef)
	}
	subFlow, ok := versions[flowVersion]
	if !ok {
		return nil, fmt.Errorf("subflow node: flow %q version %q not found in bundle", flowName, flowVersion)
	}

	subInputs, err := resolveNodeInputs(node, r.execCtx)
	if err != nil {
		return nil, fmt.Errorf("subflow node: %w", err)
	}

	subStart := time.Now()
	r.tracer.Emit(TraceEvent{Event: "subflow_start", Node: localName, Flow: flowRef})

	subExecCtx := NewExecutionContext(subInputs)
	sub := &runner{
		b:        r.b,
		flow:     subFlow,
		execCtx:  subExecCtx,
		reg:      r.reg,
		provider: r.provider,
		nextMap:  buildNextMap(subFlow),
		tracer:   r.tracer,
		depth:    r.depth + 1,
	}

	if err := sub.runFrontier(ctx); err != nil {
		return nil, fmt.Errorf("subflow %q: %w", flowRef, err)
	}

	result, err := resolveFlowOutputs(subFlow, subExecCtx)
	if err != nil {
		return nil, fmt.Errorf("subflow %q outputs: %w", flowRef, err)
	}

	r.tracer.Emit(TraceEvent{
		Event:      "subflow_done",
		Node:       localName,
		Flow:       flowRef,
		DurationMS: time.Since(subStart).Milliseconds(),
	})
	return result, nil
}
