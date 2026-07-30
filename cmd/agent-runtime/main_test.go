package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/levelplaneai/agent-runtime/internal/runtime"
)

// writeInputsFile writes {"inputs": inputs} to a temp file and returns its path.
func writeInputsFile(t *testing.T, inputs map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "inputs.json")
	data, err := json.Marshal(map[string]any{"inputs": inputs})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

// The reason --inputs-file exists: a single argv string is capped at MAX_ARG_STRLEN
// (32 pages = 131072 bytes) on Linux, so a large object input via --input failed the
// exec with E2BIG. Through a file it arrives intact, and as a real object rather than
// a JSON string — a string would make nested bindings resolve to null silently.
//
// Also covers type fidelity in the same pass: via --input every value arrived as a
// string and needed coerceDeclaredInputs to decode it, whereas JSON carries its own
// types. One oversized fixture, both claims.
func TestInputsFileCarriesPayloadOverArgvLimit(t *testing.T) {
	big := make([]any, 0, 4000)
	for i := 0; i < 4000; i++ {
		big = append(big, map[string]any{"node": strings.Repeat("x", 40), "index": i})
	}
	priorRun := map[string]any{"slices": big}

	raw, err := json.Marshal(priorRun)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if len(raw) <= 131072 {
		t.Fatalf("fixture too small to prove the point: %d bytes, want > 131072", len(raw))
	}

	path := writeInputsFile(t, map[string]any{
		"prior_run":               priorRun,
		"batch_size":              1000,
		"company_dna_unavailable": false,
		"starting_condition":      nil,
	})
	f, err := parseRunFlags([]string{"--inputs-file", path})
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}

	got, ok := f.inputs["prior_run"].(map[string]any)
	if !ok {
		t.Fatalf("prior_run is %T, want map[string]any", f.inputs["prior_run"])
	}
	slices, ok := got["slices"].([]any)
	if !ok {
		t.Fatalf("slices is %T, want []any", got["slices"])
	}
	if len(slices) != 4000 {
		t.Errorf("slices length = %d, want 4000", len(slices))
	}
	// json.Unmarshal into any yields float64 for numbers — a real number either way,
	// unlike --input which would have delivered the string "1000".
	if v, ok := f.inputs["batch_size"].(float64); !ok || v != 1000 {
		t.Errorf("batch_size = %#v, want float64(1000)", f.inputs["batch_size"])
	}
	if v, ok := f.inputs["company_dna_unavailable"].(bool); !ok || v {
		t.Errorf("company_dna_unavailable = %#v, want false", f.inputs["company_dna_unavailable"])
	}
	if v, present := f.inputs["starting_condition"]; !present || v != nil {
		t.Errorf("starting_condition = %#v (present=%v), want nil and present", v, present)
	}
}

// --input must win over the file in BOTH orders. The parse loop is left-to-right, so
// a user can write the flags either way and must get the same result.
func TestArgvInputWinsOverInputsFileEitherOrder(t *testing.T) {
	path := writeInputsFile(t, map[string]any{"notes": "from-file", "batch_size": 1000})

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"file first", []string{"--inputs-file", path, "--input", "notes=from-argv"}},
		{"argv first", []string{"--input", "notes=from-argv", "--inputs-file", path}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, err := parseRunFlags(tc.args)
			if err != nil {
				t.Fatalf("parseRunFlags: %v", err)
			}
			if f.inputs["notes"] != "from-argv" {
				t.Errorf("notes = %#v, want %q", f.inputs["notes"], "from-argv")
			}
			// Non-conflicting keys still come through from the file.
			if v, ok := f.inputs["batch_size"].(float64); !ok || v != 1000 {
				t.Errorf("batch_size = %#v, want float64(1000)", f.inputs["batch_size"])
			}
		})
	}
}

// The file must never clobber a FileValue: prompt nodes would receive a string instead
// of document bytes, with no error to notice.
func TestInputsFileDoesNotClobberFileValue(t *testing.T) {
	pdf := filepath.Join(t.TempDir(), "drawing.pdf")
	if err := os.WriteFile(pdf, []byte("%PDF-1.4 fake"), 0o600); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	path := writeInputsFile(t, map[string]any{"drawing": "/some/other/path.pdf"})

	f, err := parseRunFlags([]string{"--input", "drawing=@" + pdf, "--inputs-file", path})
	if err != nil {
		t.Fatalf("parseRunFlags: %v", err)
	}
	fv, ok := f.inputs["drawing"].(runtime.FileValue)
	if !ok {
		t.Fatalf("drawing is %T, want runtime.FileValue", f.inputs["drawing"])
	}
	if fv.MediaType != "application/pdf" {
		t.Errorf("MediaType = %q, want application/pdf", fv.MediaType)
	}
	if string(fv.Data) != "%PDF-1.4 fake" {
		t.Errorf("Data = %q, want the file's bytes", fv.Data)
	}
}

func TestInputsFileErrors(t *testing.T) {
	t.Run("missing path argument", func(t *testing.T) {
		if _, err := parseRunFlags([]string{"--inputs-file"}); err == nil {
			t.Fatal("want error, got nil")
		}
	})

	t.Run("unreadable file", func(t *testing.T) {
		_, err := parseRunFlags([]string{"--inputs-file", filepath.Join(t.TempDir(), "nope.json")})
		if err == nil || !strings.Contains(err.Error(), "--inputs-file") {
			t.Fatalf("want an --inputs-file error, got %v", err)
		}
	})

	t.Run("malformed JSON", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad.json")
		if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		_, err := parseRunFlags([]string{"--inputs-file", path})
		if err == nil || !strings.Contains(err.Error(), "--inputs-file") {
			t.Fatalf("want an --inputs-file error, got %v", err)
		}
	})

	t.Run("absent inputs key is not an error", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty.json")
		if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		f, err := parseRunFlags([]string{"--inputs-file", path})
		if err != nil {
			t.Fatalf("parseRunFlags: %v", err)
		}
		if len(f.inputs) != 0 {
			t.Errorf("inputs = %#v, want empty", f.inputs)
		}
	})
}
