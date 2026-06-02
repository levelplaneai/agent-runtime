package runtime

import (
	"context"
	"fmt"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// executeLoop runs a loop node: iterates sequentially over an initially-resolved
// array, executing the do-node once per item. Unlike map, the queue can grow
// during execution — if the do-node's output contains a non-empty []any at the
// key named by config.append_from, those items are appended to the queue and
// processed in turn. Results accumulate in the order they complete under the key
// named by config.accumulate (default "items").
//
// This models the sequential-feedback-loop-with-dynamic-queue pattern where each
// iteration can discover new work (e.g. feature verification discovering new features).
//
// Config fields:
//
//	over        (required) — path to the initial []any queue, e.g. "$.phase4.output.names"
//	as          (required) — iteration variable name, accessible as $.name in the do-node
//	do          (required) — local node name to execute per item
//	append_from (optional) — key in the do-node output that holds []any of new items
//	accumulate  (optional) — key name in the loop output for collected results (default "items")
func (r *runner) executeLoop(ctx context.Context, localName string, loopNode bundle.Node) (map[string]any, error) {
	overPath, err := configString(loopNode.Config, "over")
	if err != nil {
		return nil, fmt.Errorf("loop node: %w", err)
	}
	asName, err := configString(loopNode.Config, "as")
	if err != nil {
		return nil, fmt.Errorf("loop node: %w", err)
	}
	doName, err := configString(loopNode.Config, "do")
	if err != nil {
		return nil, fmt.Errorf("loop node: %w", err)
	}
	appendFrom, _, err := configOptionalString(loopNode.Config, "append_from")
	if err != nil {
		return nil, fmt.Errorf("loop node: %w", err)
	}
	accumulate, _, err := configOptionalString(loopNode.Config, "accumulate")
	if err != nil {
		return nil, fmt.Errorf("loop node: %w", err)
	}
	if accumulate == "" {
		accumulate = "items"
	}

	raw, err := Resolve(r.execCtx, overPath)
	if err != nil {
		return nil, fmt.Errorf("loop node: resolving 'over': %w", err)
	}
	initial, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("loop node: 'over' must resolve to an array, got %T", raw)
	}

	// Copy initial items into a local queue so appended items do not affect the
	// source value stored in the execution context.
	queue := make([]any, len(initial))
	copy(queue, initial)

	r.tracer.Emit(TraceEvent{Event: "loop_start", Node: localName, ItemCount: len(queue)})

	// Disable mid-loop checkpoints inside the loop body for the same reason map does:
	// the outer checkpoint closure references the frontier/visited of the enclosing flow,
	// not the loop's internal state.
	prev := r.midLoopCheckpoint
	r.midLoopCheckpoint = nil
	defer func() { r.midLoopCheckpoint = prev }()

	results := make([]any, 0, len(queue))
	for idx := 0; idx < len(queue); idx++ {
		r.execCtx.SetIterVar(asName, queue[idx])
		out, _, err := r.executeNode(ctx, doName)
		r.execCtx.ClearIterVar(asName)
		if err != nil {
			return nil, fmt.Errorf("loop node: item %d: %w", idx, err)
		}
		results = append(results, out)

		r.tracer.Emit(TraceEvent{Event: "loop_item_done", Node: localName, ItemIndex: idx + 1, ItemCount: len(queue)})

		// Extend the queue with any new items discovered by this iteration.
		if appendFrom != "" {
			if newRaw, ok := out[appendFrom]; ok {
				if newItems, ok := newRaw.([]any); ok && len(newItems) > 0 {
					queue = append(queue, newItems...)
					r.tracer.Emit(TraceEvent{Event: "loop_queue_extended", Node: localName, ItemCount: len(queue)})
				}
			}
		}
	}

	return map[string]any{accumulate: results}, nil
}
