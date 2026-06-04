package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// configOptionalString extracts a JSON string from a node's config map.
// Returns ("", false, nil) when the key is absent.
func configOptionalString(config map[string]json.RawMessage, key string) (string, bool, error) {
	raw, ok := config[key]
	if !ok {
		return "", false, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", true, fmt.Errorf("config field %q must be a string: %w", key, err)
	}
	return s, true, nil
}

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

	tool, sig, ok := reg.Lookup(toolRef)
	if !ok {
		return nil, fmt.Errorf("tool_call node: tool %q not found in registry", toolRef)
	}

	// Apply per-tool timeout from the tool signature when set.
	if sig.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, time.Duration(sig.TimeoutSeconds)*time.Second)
		defer cancel()
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
		switch binding.Type {
		case "file_path":
			fv, err := loadFileValue(val)
			if err != nil {
				return nil, fmt.Errorf("resolving input %q: %w", name, err)
			}
			resolved[name] = fv
		case "file_path_array":
			raw, ok := val.([]any)
			if !ok {
				return nil, fmt.Errorf("resolving input %q: \"file_path_array\" requires an array, got %T", name, val)
			}
			fvs := make([]FileValue, 0, len(raw))
			for i, elem := range raw {
				fv, err := loadFileValue(elem)
				if err != nil {
					return nil, fmt.Errorf("resolving input %q[%d]: %w", name, i, err)
				}
				fvs = append(fvs, fv)
			}
			resolved[name] = fvs
		default:
			resolved[name] = val
		}
	}
	return resolved, nil
}

// loadFileValue reads a file from the path given by val (which must be a non-empty
// string) and returns a FileValue with detected MIME type.
func loadFileValue(val any) (FileValue, error) {
	path, ok := val.(string)
	if !ok {
		return FileValue{}, fmt.Errorf("type \"file_path\" requires a string, got %T", val)
	}
	if path == "" {
		return FileValue{}, fmt.Errorf("type \"file_path\" path must not be empty")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return FileValue{}, fmt.Errorf("reading file %q: %w", path, err)
	}
	return FileValue{
		Name:      filepath.Base(path),
		Data:      data,
		MediaType: detectMIMEType(path, data),
	}, nil
}

// detectMIMEType returns the MIME type for the given path and data.
// Extension-based detection is tried first for types that net/http.DetectContentType misses.
func detectMIMEType(path string, data []byte) string {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".md", ".markdown":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".html", ".htm":
		return "text/html"
	case ".xml":
		return "text/xml"
	case ".json":
		return "application/json"
	}
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	return http.DetectContentType(sniff)
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
