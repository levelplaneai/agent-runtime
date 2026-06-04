package runtime

import (
	"fmt"
	"strings"
)

// Resolve evaluates a "from" path against the execution context and returns the value.
//
// Supported forms:
//
//	$.inputs.<field>              — a named flow input
//	$.inputs.<field>.<path>...    — nested traversal into a flow input
//	$.<node>.output               — a node's full output
//	$.<node>.output.<path>...     — nested traversal into a node output
func Resolve(ctx *ExecutionContext, path string) (any, error) {
	if !strings.HasPrefix(path, "$.") {
		return nil, fmt.Errorf("path %q: must start with $.", path)
	}

	parts := strings.Split(path[2:], ".")
	root := parts[0]

	// A bare $.<name> with no sub-field is only valid as a reference to an
	// iteration variable set by a map or loop node (e.g. "$.item"). Check that
	// before emitting the generic "too short" error so $.<as_name> resolves to
	// the whole current item, matching the documented behaviour in FLOWS.md.
	if len(parts) < 2 {
		if val, ok := ctx.IterVar(root); ok {
			return val, nil
		}
		return nil, fmt.Errorf("path %q: too short — expected $.inputs.<field>, $.<node>.output[.<path>], or $.<iter_var>", path)
	}

	switch root {
	case "inputs":
		field := parts[1]
		if field == "" {
			return nil, fmt.Errorf("path %q: $.inputs requires a field name", path)
		}
		val, ok := ctx.Inputs()[field]
		if !ok {
			return nil, fmt.Errorf("path %q: input field %q not found", path, field)
		}
		if len(parts) > 2 {
			return traverse(val, parts[2:], path)
		}
		return val, nil

	default:
		// Check iter vars first: $.<as_name>.<field>... (set by map nodes during iteration)
		if val, ok := ctx.IterVar(root); ok {
			if len(parts) > 1 {
				return traverse(val, parts[1:], path)
			}
			return val, nil
		}

		// $.<node>.output[.<path>...]
		if len(parts) < 2 || parts[1] != "output" {
			return nil, fmt.Errorf("path %q: node reference must be $.<node>.output[.<path>]", path)
		}
		output, ok := ctx.NodeOutput(root)
		if !ok {
			return nil, fmt.Errorf("path %q: no output recorded for node %q", path, root)
		}
		if len(parts) > 2 {
			return traverse(output, parts[2:], path)
		}
		return output, nil
	}
}

// traverse walks nested map[string]any values following the given key segments.
func traverse(val any, keys []string, fullPath string) (any, error) {
	cur := val
	for i, key := range keys {
		m, ok := cur.(map[string]any)
		if !ok {
			traveled := fullPath[:len(fullPath)-countSuffix(keys[i:])]
			return nil, fmt.Errorf("path %q: cannot index into %T at %q", fullPath, cur, traveled)
		}
		next, exists := m[key]
		if !exists {
			return nil, fmt.Errorf("path %q: key %q not found", fullPath, key)
		}
		cur = next
	}
	return cur, nil
}

// countSuffix returns the length of ".key1.key2..." for the given remaining keys.
func countSuffix(keys []string) int {
	n := 0
	for _, k := range keys {
		n += 1 + len(k) // leading dot + key
	}
	return n
}
