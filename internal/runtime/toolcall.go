package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// ExecuteToolCall runs a tool_call node against the execution context.
//
// It:
//  1. Resolves each declared node input via its "from" binding.
//  2. Extracts config.tool (the "name@version" tool ref) and config.args.
//  3. Renders {{ name }} placeholders in string arg values using the resolved inputs.
//  4. Looks up the tool in the registry and calls it.
//  5. Returns the tool's output map, which the caller stores via SetNodeOutput.
func ExecuteToolCall(ctx context.Context, node bundle.Node, execCtx *ExecutionContext, reg *Registry) (map[string]any, error) {
	resolved, err := resolveNodeInputs(node, execCtx)
	if err != nil {
		return nil, err
	}

	toolRef, err := configString(node.Config, "tool")
	if err != nil {
		return nil, fmt.Errorf("tool_call node: %w", err)
	}

	args, err := renderArgs(node.Config, resolved)
	if err != nil {
		return nil, fmt.Errorf("tool_call node %q: %w", toolRef, err)
	}

	tool, _, ok := reg.Lookup(toolRef)
	if !ok {
		return nil, fmt.Errorf("tool_call node: tool %q not found in registry", toolRef)
	}

	t := tracerFrom(ctx)
	t.Emit(TraceEvent{Event: "tool_start", Node: execCtx.CurrentNode(), Tool: toolRef, Args: args})
	toolStart := time.Now()

	output, err := tool.Call(ctx, args)
	if err != nil {
		return nil, fmt.Errorf("tool_call node: tool %q returned error: %w", toolRef, err)
	}

	t.Emit(TraceEvent{
		Event:      "tool_done",
		Node:       execCtx.CurrentNode(),
		Tool:       toolRef,
		DurationMS: time.Since(toolStart).Milliseconds(),
	})
	return output, nil
}

// resolveNodeInputs evaluates every "from" binding on a node and returns a
// map from input name → resolved value.
func resolveNodeInputs(node bundle.Node, execCtx *ExecutionContext) (map[string]any, error) {
	resolved := make(map[string]any, len(node.Inputs))
	for name, binding := range node.Inputs {
		val, err := Resolve(execCtx, binding.From)
		if err != nil {
			return nil, fmt.Errorf("resolving input %q: %w", name, err)
		}
		resolved[name] = val
	}
	return resolved, nil
}

// configInt extracts a JSON integer value from a node's config map.
// Returns (0, false, nil) when the key is absent (caller treats it as unset).
func configInt(config map[string]json.RawMessage, key string) (int, bool, error) {
	raw, ok := config[key]
	if !ok {
		return 0, false, nil
	}
	var v int
	if err := json.Unmarshal(raw, &v); err != nil {
		return 0, true, fmt.Errorf("config field %q must be an integer: %w", key, err)
	}
	return v, true, nil
}

// configString extracts a JSON string value from a node's config map.
func configString(config map[string]json.RawMessage, key string) (string, error) {
	raw, ok := config[key]
	if !ok {
		return "", fmt.Errorf("config missing required field %q", key)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", fmt.Errorf("config field %q must be a string: %w", key, err)
	}
	return s, nil
}

// renderArgs unmarshals config.args and renders {{ name }} placeholders in
// string arg values using the resolved inputs. Non-string values are passed
// through unchanged.
func renderArgs(config map[string]json.RawMessage, inputs map[string]any) (map[string]any, error) {
	raw, ok := config["args"]
	if !ok {
		return map[string]any{}, nil
	}

	var rawArgs map[string]json.RawMessage
	if err := json.Unmarshal(raw, &rawArgs); err != nil {
		return nil, fmt.Errorf("config.args must be an object: %w", err)
	}

	args := make(map[string]any, len(rawArgs))
	for k, v := range rawArgs {
		var s string
		if err := json.Unmarshal(v, &s); err == nil {
			// Value is a JSON string — render any {{ name }} placeholders.
			rendered, err := Render(s, inputs)
			if err != nil {
				return nil, fmt.Errorf("arg %q: %w", k, err)
			}
			args[k] = rendered
		} else {
			// Non-string value — unmarshal as generic any.
			var generic any
			if err := json.Unmarshal(v, &generic); err != nil {
				return nil, fmt.Errorf("arg %q: cannot unmarshal: %w", k, err)
			}
			args[k] = generic
		}
	}
	return args, nil
}
