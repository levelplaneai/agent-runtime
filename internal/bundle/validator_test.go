package bundle

import (
	"strings"
	"testing"
)

// bundleWithOutputs builds a minimal single-node bundle whose entry flow has the
// given output bindings. The node "n" always exists so from paths referencing it
// are valid.
func bundleWithOutputs(outputs map[string]FlowOutputBinding) *Bundle {
	return &Bundle{
		Manifest: Manifest{Entry: "main@v1"},
		Flows: map[string]map[string]Flow{
			"main": {
				"v1": {
					Entry:   "n",
					Nodes:   map[string]string{"n": "impl@v1"},
					Outputs: outputs,
				},
			},
		},
		Nodes: map[string]map[string]Node{
			"impl": {"v1": {Type: "tool_call"}},
		},
		Tools: map[string]map[string]ToolSignature{},
	}
}

// validateErrString runs Validate and joins all errors into one string for
// substring assertions.
func validateErrString(b *Bundle) string {
	var sb strings.Builder
	for _, e := range Validate(b) {
		sb.WriteString(e.Error())
		sb.WriteString("\n")
	}
	return sb.String()
}

func TestValidate_FromAny_Valid(t *testing.T) {
	b := bundleWithOutputs(map[string]FlowOutputBinding{
		"result": {FromAny: []string{"$.n.output.a", "$.inputs.x", "$.n.output.b"}},
	})
	if errs := Validate(b); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}

func TestValidate_FromAny_And_From_Both(t *testing.T) {
	b := bundleWithOutputs(map[string]FlowOutputBinding{
		"result": {From: "$.n.output", FromAny: []string{"$.n.output.a"}},
	})
	got := validateErrString(b)
	if !strings.Contains(got, "exactly one of from / from_any") {
		t.Fatalf("expected both-set error, got %q", got)
	}
}

func TestValidate_FromAny_Neither(t *testing.T) {
	b := bundleWithOutputs(map[string]FlowOutputBinding{
		"result": {},
	})
	got := validateErrString(b)
	if !strings.Contains(got, "from or from_any is required") {
		t.Fatalf("expected neither-set error, got %q", got)
	}
}

func TestValidate_FromAny_EmptyArray(t *testing.T) {
	b := bundleWithOutputs(map[string]FlowOutputBinding{
		"result": {FromAny: []string{}},
	})
	got := validateErrString(b)
	if !strings.Contains(got, "from_any must be a non-empty array") {
		t.Fatalf("expected empty-array error, got %q", got)
	}
}

func TestValidate_FromAny_UnknownNode(t *testing.T) {
	b := bundleWithOutputs(map[string]FlowOutputBinding{
		"result": {FromAny: []string{"$.n.output.a", "$.ghost.output"}},
	})
	got := validateErrString(b)
	if !strings.Contains(got, "unknown node \"ghost\"") {
		t.Fatalf("expected unknown-node error, got %q", got)
	}
	if !strings.Contains(got, "from_any[1]") {
		t.Fatalf("expected error to point at from_any[1], got %q", got)
	}
}

func TestValidate_From_StillWorks(t *testing.T) {
	b := bundleWithOutputs(map[string]FlowOutputBinding{
		"result": {From: "$.n.output"},
	})
	if errs := Validate(b); len(errs) != 0 {
		t.Fatalf("expected no errors, got %v", errs)
	}
}
