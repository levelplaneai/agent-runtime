package runtime

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/aditya-vinodh/agent-runtime/internal/bundle"
)

type errorPolicy struct {
	action  string // "fail", "skip", "retry"
	retries int    // additional attempts; only meaningful when action == "retry"
}

// parseErrorPolicy parses the node's on_error string.
// Valid values: "", "fail", "skip", "retry:N" where N >= 1.
func parseErrorPolicy(raw string) (errorPolicy, error) {
	switch raw {
	case "", "fail":
		return errorPolicy{action: "fail"}, nil
	case "skip":
		return errorPolicy{action: "skip"}, nil
	}
	if strings.HasPrefix(raw, "retry:") {
		nStr := strings.TrimPrefix(raw, "retry:")
		n, err := strconv.Atoi(nStr)
		if err != nil || n <= 0 {
			return errorPolicy{}, fmt.Errorf("on_error %q: retry count must be a positive integer", raw)
		}
		return errorPolicy{action: "retry", retries: n}, nil
	}
	return errorPolicy{}, fmt.Errorf("on_error %q: unknown policy (want \"fail\", \"skip\", or \"retry:N\")", raw)
}

// ApplyErrorPolicy executes fn according to the node's on_error policy.
// localName is the flow-local node name, used for trace events.
//
//   - "fail" (default): propagate the error from fn unchanged.
//   - "skip": if fn errors, return (map[string]any{}, nil) so the flow continues.
//     Downstream paths that read this node's output will resolve to an empty map;
//     field-level accesses (e.g. $.<node>.output.field) will error at resolve time.
//   - "retry:N": call fn up to N additional times on failure (N+1 total attempts).
//     Context cancellation is checked between attempts. Returns the last error if
//     all attempts fail.
func ApplyErrorPolicy(ctx context.Context, localName string, node bundle.Node, fn func() (map[string]any, error)) (map[string]any, error) {
	policy, err := parseErrorPolicy(node.OnError)
	if err != nil {
		return nil, err
	}

	t := tracerFrom(ctx)

	switch policy.action {
	case "fail":
		return fn()

	case "skip":
		out, err := fn()
		if err != nil {
			t.Emit(TraceEvent{Event: "node_skip", Node: localName, Error: err.Error()})
			return map[string]any{}, nil
		}
		return out, nil

	case "retry":
		var lastErr error
		for attempt := 0; attempt <= policy.retries; attempt++ {
			if attempt > 0 {
				if ctx.Err() != nil {
					return nil, ctx.Err()
				}
			}
			out, err := fn()
			if err == nil {
				return out, nil
			}
			lastErr = err
			if attempt < policy.retries {
				t.Emit(TraceEvent{
					Event:      "node_retry",
					Node:       localName,
					Attempt:    attempt + 1,
					MaxRetries: policy.retries,
					Error:      err.Error(),
				})
			}
		}
		return nil, lastErr

	default:
		// unreachable — parseErrorPolicy only returns known actions
		return fn()
	}
}
