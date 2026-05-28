package runtime

import (
	"encoding/base64"
	"sort"
	"time"
)

// Snapshot captures the full execution state of a flow at a node boundary so
// that a run can be paused and later resumed via RunFlowResume.
type Snapshot struct {
	RunID         string         `json:"run_id"`
	Timestamp     time.Time      `json:"timestamp"`
	BundleVersion string         `json:"bundle_version"`
	FlowRef       string         `json:"flow_ref"` // "name@version"
	Inputs        map[string]any `json:"inputs"`
	NodeOutputs   map[string]any `json:"node_outputs"`
	Visited       []string       `json:"visited"`  // sorted; all nodes completed so far
	Frontier      []string       `json:"frontier"` // nodes still to execute
	ActiveNode    *NodeSnapshot  `json:"active_node,omitempty"` // reserved for Feature 4 mid-loop
}

// NodeSnapshot holds mid-loop state for an agentic prompt node (Feature 4).
// Populated only when a checkpoint is taken inside a tool-use loop.
type NodeSnapshot struct {
	NodeName  string    `json:"node_name"`
	NodeType  string    `json:"node_type"`
	Messages  []Message `json:"messages,omitempty"`
	Iteration int       `json:"iteration,omitempty"`
}

// marshalAnyMap deep-converts a map[string]any so it can be safely
// round-tripped through JSON. FileValue and ToolImageOutput values contain
// []byte fields that json.Unmarshal would otherwise deserialize back as
// map[string]interface{}, losing their type. Both are converted to a tagged
// map with a "__type" sentinel so unmarshalAnyMap can reconstruct them.
func marshalAnyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = marshalAnyValue(v)
	}
	return out
}

func marshalAnyValue(v any) any {
	switch val := v.(type) {
	case FileValue:
		return map[string]any{
			"__type":     "file_value",
			"name":       val.Name,
			"data":       base64.StdEncoding.EncodeToString(val.Data),
			"media_type": val.MediaType,
		}
	case *FileValue:
		if val == nil {
			return nil
		}
		return marshalAnyValue(*val)
	case ToolImageOutput:
		return map[string]any{
			"__type":     "tool_image",
			"data":       base64.StdEncoding.EncodeToString(val.Data),
			"media_type": val.MediaType,
		}
	case *ToolImageOutput:
		if val == nil {
			return nil
		}
		return marshalAnyValue(*val)
	case map[string]any:
		return marshalAnyMap(val)
	case []any:
		out := make([]any, len(val))
		for i, item := range val {
			out[i] = marshalAnyValue(item)
		}
		return out
	default:
		return v
	}
}

// unmarshalAnyMap reverses marshalAnyMap, reconstructing FileValue and
// ToolImageOutput values from their tagged-map JSON representation.
func unmarshalAnyMap(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = unmarshalAnyValue(v)
	}
	return out
}

func unmarshalAnyValue(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		if arr, ok := v.([]any); ok {
			out := make([]any, len(arr))
			for i, item := range arr {
				out[i] = unmarshalAnyValue(item)
			}
			return out
		}
		return v
	}
	switch m["__type"] {
	case "file_value":
		data, _ := base64.StdEncoding.DecodeString(stringVal(m["data"]))
		return FileValue{
			Name:      stringVal(m["name"]),
			Data:      data,
			MediaType: stringVal(m["media_type"]),
		}
	case "tool_image":
		data, _ := base64.StdEncoding.DecodeString(stringVal(m["data"]))
		return ToolImageOutput{
			Data:      data,
			MediaType: stringVal(m["media_type"]),
		}
	default:
		return unmarshalAnyMap(m)
	}
}

func stringVal(v any) string {
	s, _ := v.(string)
	return s
}

// sliceToSet converts a []string to a map[string]bool lookup set.
func sliceToSet(s []string) map[string]bool {
	out := make(map[string]bool, len(s))
	for _, v := range s {
		out[v] = true
	}
	return out
}

// sortedKeys returns a sorted slice of the keys from a map[string]bool.
func sortedKeys(m map[string]bool) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
