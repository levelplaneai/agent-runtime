package runtime

import (
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// traceNodeInputs must (1) resolve plain bindings to their values, (2) turn a
// file_path binding into a byte-free {type:"file", path} marker WITHOUT reading
// the file (the path here does not exist on disk), and (3) silently skip an
// unresolvable binding rather than error — a trace must never break a run.
func TestTraceNodeInputs(t *testing.T) {
	ctx := NewExecutionContext(map[string]any{
		"batch_size":   10000,
		"prior_run":    map[string]any{"process": "casting"},
		"drawing_path": "/does/not/exist.pdf",
	})

	node := bundle.Node{
		Inputs: map[string]bundle.InputBinding{
			"batch":   {From: "$.inputs.batch_size"},
			"prior":   {From: "$.inputs.prior_run"},
			"drawing": {From: "$.inputs.drawing_path", Type: "file_path"},
			"missing": {From: "$.inputs.not_a_field"},
		},
	}

	got := traceNodeInputs(node, ctx)

	if got["batch"] != 10000 {
		t.Errorf("batch: got %v, want 10000", got["batch"])
	}
	if _, ok := got["missing"]; ok {
		t.Errorf("unresolvable binding must be skipped, got %v", got["missing"])
	}
	fileMarker, ok := got["drawing"].(map[string]any)
	if !ok || fileMarker["type"] != "file" || fileMarker["path"] != "/does/not/exist.pdf" {
		t.Errorf("drawing: want file marker with path, got %v", got["drawing"])
	}
	if _, leaked := fileMarker["Data"]; leaked {
		t.Errorf("file marker leaked bytes: %v", fileMarker)
	}
}

// A node with no declared inputs returns nil, so json:"inputs,omitempty" keeps
// node_start byte-identical for loop/map/parallel nodes.
func TestTraceNodeInputs_NoInputs(t *testing.T) {
	if got := traceNodeInputs(bundle.Node{}, NewExecutionContext(nil)); got != nil {
		t.Errorf("want nil, got %v", got)
	}
}

// sanitizeTraceValue replaces a FileValue's raw bytes with a size marker, so a
// loaded drawing rides through flow_start as ~40 bytes instead of megabytes.
func TestSanitizeTraceValue_FileValue(t *testing.T) {
	fv := FileValue{Name: "d.pdf", MediaType: "application/pdf", Data: make([]byte, 2_800_000)}
	m, ok := sanitizeTraceValue(fv).(map[string]any)
	if !ok {
		t.Fatalf("want map marker, got %T", sanitizeTraceValue(fv))
	}
	if m["type"] != "file" || m["name"] != "d.pdf" || m["size"] != 2_800_000 {
		t.Errorf("bad marker: %v", m)
	}
	if _, leaked := m["Data"]; leaked {
		t.Errorf("marker leaked bytes")
	}
	// non-file values pass through untouched
	if sanitizeTraceValue("hello") != "hello" {
		t.Errorf("plain value should pass through")
	}
}
