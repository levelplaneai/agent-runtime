package runtime

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var templatePlaceholder = regexp.MustCompile(`\{\{\s*(\w+)\s*\}\}`)

// Render substitutes {{ name }} placeholders in tmpl with corresponding values
// from inputs. Returns an error if any placeholder references a key absent from inputs.
//
// Values are converted to strings as follows:
//   - string  → verbatim
//   - number, bool → fmt default formatting
//   - object, array → compact JSON
func Render(tmpl string, inputs map[string]any) (string, error) {
	var renderErr error
	result := templatePlaceholder.ReplaceAllStringFunc(tmpl, func(match string) string {
		if renderErr != nil {
			return match
		}
		sub := templatePlaceholder.FindStringSubmatch(match)
		name := sub[1]
		val, ok := inputs[name]
		if !ok {
			renderErr = fmt.Errorf("template placeholder {{ %s }} has no corresponding input", name)
			return match
		}
		s, err := valueToString(val)
		if err != nil {
			renderErr = fmt.Errorf("template placeholder {{ %s }}: %w", name, err)
			return match
		}
		return s
	})
	if renderErr != nil {
		return "", renderErr
	}
	return result, nil
}

func valueToString(v any) (string, error) {
	switch t := v.(type) {
	case string:
		return t, nil
	case json.Number:
		return t.String(), nil
	case float64:
		return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%g", t), "0"), "."), nil
	case int:
		return fmt.Sprintf("%d", t), nil
	case int64:
		return fmt.Sprintf("%d", t), nil
	case bool:
		if t {
			return "true", nil
		}
		return "false", nil
	case FileValue:
		return t.Name, nil
	case nil:
		return "", nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return "", fmt.Errorf("cannot marshal value of type %T to string: %w", v, err)
		}
		return string(b), nil
	}
}
