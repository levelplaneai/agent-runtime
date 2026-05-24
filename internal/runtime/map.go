package runtime

import (
	"context"
	"fmt"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
	"golang.org/x/sync/errgroup"
)

// executeMap runs a map node: iterates over the array at config.over, executing
// the do-node once per item with the iteration variable (config.as) set on the
// execution context, and returns {"items": [out1, out2, ...]}.
//
// When config.concurrency > 1, iterations run in bounded parallel goroutines
// using errgroup; the first error cancels remaining goroutines. Results are
// always returned in input order regardless of completion order.
// When config.concurrency is absent or <= 1, execution is sequential.
func (r *runner) executeMap(ctx context.Context, localName string, mapNode bundle.Node) (map[string]any, error) {
	overPath, err := configString(mapNode.Config, "over")
	if err != nil {
		return nil, fmt.Errorf("map node: %w", err)
	}
	asName, err := configString(mapNode.Config, "as")
	if err != nil {
		return nil, fmt.Errorf("map node: %w", err)
	}
	doName, err := configString(mapNode.Config, "do")
	if err != nil {
		return nil, fmt.Errorf("map node: %w", err)
	}

	raw, err := Resolve(r.execCtx, overPath)
	if err != nil {
		return nil, fmt.Errorf("map node: resolving 'over': %w", err)
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("map node: 'over' must resolve to an array, got %T", raw)
	}

	concurrency, _, err := configInt(mapNode.Config, "concurrency")
	if err != nil {
		return nil, fmt.Errorf("map node: %w", err)
	}

	r.tracer.Emit(TraceEvent{Event: "map_start", Node: localName, ItemCount: len(items)})

	if concurrency > 1 {
		return r.executeMapConcurrent(ctx, localName, asName, doName, items, concurrency)
	}
	return r.executeMapSequential(ctx, localName, asName, doName, items)
}

func (r *runner) executeMapSequential(ctx context.Context, localName, asName, doName string, items []any) (map[string]any, error) {
	results := make([]any, 0, len(items))
	for i, item := range items {
		r.execCtx.SetIterVar(asName, item)
		out, _, err := r.executeNode(ctx, doName)
		if err != nil {
			r.execCtx.ClearIterVar(asName)
			return nil, fmt.Errorf("map node: item: %w", err)
		}
		results = append(results, out)
		r.tracer.Emit(TraceEvent{Event: "map_item_done", Node: localName, ItemIndex: i + 1, ItemCount: len(items)})
	}
	r.execCtx.ClearIterVar(asName)
	return map[string]any{"items": results}, nil
}

func (r *runner) executeMapConcurrent(ctx context.Context, localName, asName, doName string, items []any, concurrency int) (map[string]any, error) {
	g, gctx := errgroup.WithContext(ctx)
	sem := make(chan struct{}, concurrency)
	results := make([]any, len(items))

	for i, item := range items {
		i, item := i, item
		g.Go(func() error {
			select {
			case sem <- struct{}{}:
			case <-gctx.Done():
				return gctx.Err()
			}
			defer func() { <-sem }()

			clone := r.execCtx.Clone()
			clone.SetIterVar(asName, item)
			sub := &runner{
				b:        r.b,
				flow:     r.flow,
				execCtx:  clone,
				reg:      r.reg,
				provider: r.provider,
				nextMap:  r.nextMap,
				tracer:   r.tracer,
			}
			out, _, err := sub.executeNode(gctx, doName)
			if err != nil {
				return err
			}
			results[i] = out
			r.tracer.Emit(TraceEvent{Event: "map_item_done", Node: localName, ItemIndex: i + 1, ItemCount: len(items)})
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	return map[string]any{"items": results}, nil
}
