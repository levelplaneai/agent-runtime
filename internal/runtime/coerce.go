package runtime

import (
	"encoding/json"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// coerceDeclaredInputs converts CLI-stringified flow inputs back to their declared types.
//
// Flow inputs cross the SDK→binary boundary as CLI --input flags. The Python SDK json.dumps'd
// object/array/bool values into strings, and the CLI (main.go) stored them verbatim as strings.
// So an input the flow declares as "object" (e.g. prior_run) arrives here as a JSON *string* —
// which a nested binding like `$.inputs.prior_run.cast_analyze` cannot index into, silently
// yielding null. This restores the real type before the value enters the execution context.
//
// Type-guided and conservative: only inputs the flow declares as a structured/scalar-JSON type
// (object/array/number/integer/boolean) are decoded, and only when the value is currently a
// string. Declared "string"/"file" inputs, undeclared inputs, already-native values (e.g. a
// FileValue), and strings that don't parse as JSON are all left untouched. So a plain-text input
// that happens to look like JSON is never mangled, and a number renders identically either way.
func coerceDeclaredInputs(inputs map[string]any, decl map[string]bundle.FlowInputField) map[string]any {
	for key, field := range decl {
		s, isString := inputs[key].(string)
		if !isString {
			continue // native value (or absent) — nothing to decode
		}
		switch field.Type {
		case "object", "array", "number", "integer", "boolean":
			var parsed any
			if err := json.Unmarshal([]byte(s), &parsed); err == nil {
				inputs[key] = parsed
			}
			// on parse failure, keep the original string (best-effort)
		}
	}
	return inputs
}
