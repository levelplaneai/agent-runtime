package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/aditya-vinodh/agent-runtime/internal/bundle"
	"golang.org/x/sync/errgroup"
)

// executeParallel runs a parallel node: executes each named branch concurrently,
// merges their outputs into a single map keyed by branch name, and returns it.
//
// All branches start at the same time with no concurrency limit — the set is
// fixed and bounded. Each branch gets a clone of the current execution context
// so branches cannot observe each other's in-progress outputs.
//
// The first branch error cancels remaining branches (errgroup semantics).
func (r *runner) executeParallel(ctx context.Context, localName string, node bundle.Node) (map[string]any, error) {
	raw, ok := node.Config["branches"]
	if !ok {
		return nil, fmt.Errorf("parallel node: config missing required field \"branches\"")
	}
	var branches map[string]string
	if err := json.Unmarshal(raw, &branches); err != nil {
		return nil, fmt.Errorf("parallel node: config.branches must be an object: %w", err)
	}

	r.tracer.Emit(TraceEvent{Event: "parallel_start", Node: localName, ItemCount: len(branches)})

	g, gctx := errgroup.WithContext(ctx)
	results := make(map[string]any, len(branches))
	var mu sync.Mutex

	for branchName, nodeLocalName := range branches {
		branchName, nodeLocalName := branchName, nodeLocalName
		g.Go(func() error {
			clone := r.execCtx.Clone()
			sub := &runner{
				b:        r.b,
				flow:     r.flow,
				execCtx:  clone,
				reg:      r.reg,
				provider: r.provider,
				nextMap:  r.nextMap,
				tracer:   r.tracer,
			}
			out, _, err := sub.executeNode(gctx, nodeLocalName)
			if err != nil {
				return fmt.Errorf("branch %q: %w", branchName, err)
			}
			r.tracer.Emit(TraceEvent{Event: "parallel_branch_done", Node: localName, BranchName: branchName})
			mu.Lock()
			results[branchName] = out
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}
