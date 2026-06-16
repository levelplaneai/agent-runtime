package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// Condition is a structured predicate evaluated against the resolved input map.
type Condition struct {
	Field string          `json:"field"`
	Op    string          `json:"op"`
	Value json.RawMessage `json:"value,omitempty"`
}

// String returns a human-readable representation for trace output.
func (c Condition) String() string {
	if c.Op == "exists" {
		return c.Field + " exists"
	}
	return fmt.Sprintf("%s %s %s", c.Field, c.Op, string(c.Value))
}

// Branch is one conditional arm of a router node.
type Branch struct {
	When    *Condition `json:"when,omitempty"`
	Default bool       `json:"default"`
	Goto    string     `json:"goto"`
}

// decideWith holds the parsed config for an LLM-based router.
type decideWith struct {
	Model           string   `json:"model"`
	Prompt          string   `json:"prompt"`
	Choices         []string `json:"choices"`
	MaxTokens       int      `json:"max_tokens,omitempty"`
	ThinkingBudget  *int     `json:"thinking_budget,omitempty"`
	ReasoningEffort *string  `json:"reasoning_effort,omitempty"`
}

var knownOps = map[string]bool{
	"eq": true, "ne": true,
	"gt": true, "gte": true, "lt": true, "lte": true,
	"contains": true, "in": true, "exists": true,
	"length_eq": true, "length_ne": true,
	"length_gt": true, "length_gte": true,
	"length_lt": true, "length_lte": true,
}

// ExecuteRouter evaluates a router node's branches in order and returns a
// map containing the "_goto" key set to the chosen target's local name.
// The caller (executeNode) extracts and strips this key before storing output.
//
// nodeDir is the node version directory (used to load the classify prompt when
// decide_with.prompt is a file reference). provider is required when the node
// config contains a decide_with block; it may be nil for deterministic routers.
func ExecuteRouter(ctx context.Context, localName string, node bundle.Node, nodeDir string, execCtx *ExecutionContext, provider LLMProvider) (map[string]any, error) {
	branches, err := parseBranches(node.Config)
	if err != nil {
		return nil, err
	}

	resolved, err := resolveNodeInputs(node, execCtx)
	if err != nil {
		return nil, err
	}

	// LLM-based routing: classify input and inject decision into resolved map.
	if raw, ok := node.Config["decide_with"]; ok {
		decision, model, err := executeLLMDecision(ctx, localName, raw, nodeDir, resolved, provider)
		if err != nil {
			return nil, fmt.Errorf("router: decide_with: %w", err)
		}
		resolved["decision"] = decision
		tracerFrom(ctx).Emit(TraceEvent{
			Event:        "router_llm_decision",
			Node:         localName,
			Model:        model,
			ChosenTarget: decision,
		})
	}

	buildResult := func(gotoTarget string) map[string]any {
		result := map[string]any{"_goto": gotoTarget}
		if d, ok := resolved["decision"]; ok {
			result["decision"] = d
		}
		return result
	}

	t := tracerFrom(ctx)
	for _, b := range branches {
		if b.Default {
			t.Emit(TraceEvent{Event: "router_branch", Node: localName, ChosenTarget: b.Goto})
			return buildResult(b.Goto), nil
		}
		match, err := evalCondition(b.When, resolved)
		if err != nil {
			return nil, err
		}
		if match {
			t.Emit(TraceEvent{Event: "router_branch", Node: localName, Condition: b.When.String(), ChosenTarget: b.Goto})
			return buildResult(b.Goto), nil
		}
	}
	return nil, fmt.Errorf("router: no branch matched and no default branch set")
}

// executeLLMDecision calls the model to classify the router's inputs and returns
// the chosen decision string along with the model name used.
func executeLLMDecision(
	ctx context.Context,
	localName string,
	raw json.RawMessage,
	nodeDir string,
	resolved map[string]any,
	provider LLMProvider,
) (decision string, model string, err error) {
	var dw decideWith
	if err := json.Unmarshal(raw, &dw); err != nil {
		return "", "", fmt.Errorf("invalid decide_with config: %w", err)
	}
	if dw.Model == "" {
		return "", "", fmt.Errorf("decide_with.model is required")
	}
	if dw.Prompt == "" {
		return "", "", fmt.Errorf("decide_with.prompt is required")
	}
	if len(dw.Choices) == 0 {
		return "", "", fmt.Errorf("decide_with.choices must not be empty")
	}
	if dw.ReasoningEffort != nil {
		switch *dw.ReasoningEffort {
		case "low", "medium", "high":
		default:
			return "", "", fmt.Errorf("decide_with.reasoning_effort must be \"low\", \"medium\", or \"high\", got %q", *dw.ReasoningEffort)
		}
	}
	if dw.ThinkingBudget != nil && *dw.ThinkingBudget < 0 {
		return "", "", fmt.Errorf("decide_with.thinking_budget must be >= 0, got %d", *dw.ThinkingBudget)
	}

	// Load classify prompt — file reference or inline string.
	promptText := dw.Prompt
	if strings.HasPrefix(dw.Prompt, "./") {
		path := filepath.Join(nodeDir, strings.TrimPrefix(dw.Prompt, "./"))
		data, err := os.ReadFile(path)
		if err != nil {
			return "", "", fmt.Errorf("reading prompt file %q: %w", path, err)
		}
		promptText = string(data)
	}

	rendered, err := Render(promptText, resolved)
	if err != nil {
		return "", "", fmt.Errorf("rendering classify prompt: %w", err)
	}

	fileBlocks := collectFileBlocks(resolved)
	var userMsg Message
	if len(fileBlocks) == 0 {
		userMsg = Message{Role: "user", Content: rendered}
	} else {
		blocks := make([]ContentBlock, 0, 1+len(fileBlocks))
		blocks = append(blocks, ContentBlock{Type: "text", Text: rendered})
		blocks = append(blocks, fileBlocks...)
		userMsg = Message{Role: "user", Blocks: blocks}
	}

	maxTokens := 8192
	if dw.MaxTokens > 0 {
		maxTokens = dw.MaxTokens
	}

	temp := 0.0
	req := CompletionRequest{
		Model:           dw.Model,
		MaxTokens:       maxTokens,
		System:          "You are a classification assistant. Respond ONLY with valid JSON matching the requested schema. Do not include any explanation, preamble, or markdown formatting.",
		Messages:        []Message{userMsg},
		Temperature:     &temp,
		ThinkingBudget:  dw.ThinkingBudget,
		ReasoningEffort: dw.ReasoningEffort,
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"decision": map[string]any{
					"type": "string",
					"enum": dw.Choices,
				},
			},
			"required": []string{"decision"},
		},
	}

	t := tracerFrom(ctx)
	t.Emit(TraceEvent{Event: "llm_request", Node: localName, Model: req.Model, Inputs: requestToTrace(req)})
	start := time.Now()

	resp, err := provider.Complete(ctx, req)
	if err != nil {
		return "", "", fmt.Errorf("LLM call failed: %w", err)
	}

	t.Emit(TraceEvent{
		Event:        "llm_response",
		Node:         localName,
		Model:        req.Model,
		InputTokens:  resp.InputTokens,
		OutputTokens: resp.OutputTokens,
		DurationMS:   time.Since(start).Milliseconds(),
		Output:       map[string]any{"text": resp.Content},
	})

	var out struct {
		Decision string `json:"decision"`
	}
	jsonText := extractJSON(resp.Content)
	if err := json.Unmarshal([]byte(jsonText), &out); err != nil {
		return "", "", fmt.Errorf("parsing LLM response: %w; content: %s", err, resp.Content)
	}

	// Validate the model's choice is one of the declared options.
	valid := false
	for _, c := range dw.Choices {
		if c == out.Decision {
			valid = true
			break
		}
	}
	if !valid {
		return "", "", fmt.Errorf("LLM returned %q which is not in choices %v", out.Decision, dw.Choices)
	}

	return out.Decision, dw.Model, nil
}

// extractJSON returns the first JSON object found in s.
// If s is already valid JSON it is returned as-is; otherwise it looks for a
// ```…``` or ```json…``` code fence and returns the content inside it.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 0 && s[0] == '{' {
		return s
	}
	// Strip a leading ```json or ``` fence.
	for _, fence := range []string{"```json", "```"} {
		if idx := strings.Index(s, fence); idx != -1 {
			s = s[idx+len(fence):]
			if end := strings.Index(s, "```"); end != -1 {
				s = s[:end]
			}
			return strings.TrimSpace(s)
		}
	}
	return s
}

func parseBranches(config map[string]json.RawMessage) ([]Branch, error) {
	raw, ok := config["branches"]
	if !ok {
		return nil, fmt.Errorf("router: config missing 'branches'")
	}
	var branches []Branch
	if err := json.Unmarshal(raw, &branches); err != nil {
		return nil, fmt.Errorf("router: invalid 'branches': %w", err)
	}
	for i, b := range branches {
		if b.Goto == "" {
			return nil, fmt.Errorf("router: branch[%d]: goto must not be empty", i)
		}
		if b.Default && b.When != nil {
			return nil, fmt.Errorf("router: branch[%d]: cannot have both default:true and a when condition", i)
		}
		if !b.Default {
			if b.When == nil {
				return nil, fmt.Errorf("router: branch[%d]: non-default branch must have a when condition", i)
			}
			if !knownOps[b.When.Op] {
				return nil, fmt.Errorf("router: branch[%d]: unknown operator %q", i, b.When.Op)
			}
		}
	}
	return branches, nil
}

// toFloat64 converts any numeric Go value to float64. Returns false if v is not numeric.
func toFloat64(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int8:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case uint:
		return float64(n), true
	case uint8:
		return float64(n), true
	case uint16:
		return float64(n), true
	case uint32:
		return float64(n), true
	case uint64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

// numericAwareEq compares two values for equality, using float64 when both are numeric.
func numericAwareEq(a, b any) bool {
	fa, aIsNum := toFloat64(a)
	fb, bIsNum := toFloat64(b)
	if aIsNum && bIsNum {
		return fa == fb
	}
	return reflect.DeepEqual(a, b)
}

// evalCondition evaluates a structured condition against a map of resolved node inputs.
//
// Supported operators: eq, ne, gt, gte, lt, lte, contains, in, exists,
// length_eq, length_ne, length_gt, length_gte, length_lt, length_lte.
func evalCondition(c *Condition, resolved map[string]any) (bool, error) {
	if c == nil {
		return false, fmt.Errorf("evalCondition: nil condition")
	}
	if c.Field == "" {
		return false, fmt.Errorf("evalCondition: field name is empty")
	}

	fieldVal, fieldPresent := resolved[c.Field]

	switch c.Op {
	case "exists":
		if len(c.Value) > 0 && string(c.Value) != "null" {
			return false, fmt.Errorf("exists: unexpected value for field %q", c.Field)
		}
		return fieldPresent && fieldVal != nil, nil

	case "eq", "ne":
		var want any
		if err := json.Unmarshal(c.Value, &want); err != nil {
			return false, fmt.Errorf("evalCondition: invalid value for %s: %w", c.Op, err)
		}
		// null: field absent or nil both count as null
		if want == nil {
			isNull := !fieldPresent || fieldVal == nil
			if c.Op == "eq" {
				return isNull, nil
			}
			return !isNull, nil
		}
		if !fieldPresent {
			return false, fmt.Errorf("evalCondition: field %q not found", c.Field)
		}
		eq := numericAwareEq(fieldVal, want)
		if c.Op == "eq" {
			return eq, nil
		}
		return !eq, nil

	case "gt", "gte", "lt", "lte":
		if !fieldPresent {
			return false, fmt.Errorf("evalCondition: field %q not found", c.Field)
		}
		fieldF, ok := toFloat64(fieldVal)
		if !ok {
			return false, fmt.Errorf("evalCondition: field %q is not numeric", c.Field)
		}
		var wantF float64
		if err := json.Unmarshal(c.Value, &wantF); err != nil {
			return false, fmt.Errorf("evalCondition: invalid numeric value for %s: %w", c.Op, err)
		}
		switch c.Op {
		case "gt":
			return fieldF > wantF, nil
		case "gte":
			return fieldF >= wantF, nil
		case "lt":
			return fieldF < wantF, nil
		default: // lte
			return fieldF <= wantF, nil
		}

	case "contains":
		if !fieldPresent {
			return false, fmt.Errorf("evalCondition: field %q not found", c.Field)
		}
		var want any
		if err := json.Unmarshal(c.Value, &want); err != nil {
			return false, fmt.Errorf("evalCondition: invalid value for contains: %w", err)
		}
		switch fv := fieldVal.(type) {
		case string:
			s, ok := want.(string)
			if !ok {
				return false, fmt.Errorf("contains: field %q is a string but value is not", c.Field)
			}
			return strings.Contains(fv, s), nil
		case []any:
			for _, elem := range fv {
				if numericAwareEq(elem, want) {
					return true, nil
				}
			}
			return false, nil
		default:
			return false, fmt.Errorf("contains: field %q is neither string nor array, got %T", c.Field, fieldVal)
		}

	case "in":
		if !fieldPresent {
			return false, fmt.Errorf("evalCondition: field %q not found", c.Field)
		}
		var list []any
		if err := json.Unmarshal(c.Value, &list); err != nil {
			return false, fmt.Errorf("in: value must be a JSON array: %w", err)
		}
		for _, elem := range list {
			if numericAwareEq(fieldVal, elem) {
				return true, nil
			}
		}
		return false, nil

	case "length_eq", "length_ne", "length_gt", "length_gte", "length_lt", "length_lte":
		if !fieldPresent {
			return false, fmt.Errorf("evalCondition: field %q not found", c.Field)
		}
		if fieldVal == nil {
			return false, fmt.Errorf("length_*: field %q is nil", c.Field)
		}
		var wantF float64
		if err := json.Unmarshal(c.Value, &wantF); err != nil {
			return false, fmt.Errorf("evalCondition: invalid numeric value for %s: %w", c.Op, err)
		}
		n := int(wantF)
		var length int
		switch fv := fieldVal.(type) {
		case []any:
			length = len(fv)
		case string:
			length = len(fv)
		default:
			return false, fmt.Errorf("length_*: field %q is neither array nor string, got %T", c.Field, fieldVal)
		}
		switch c.Op {
		case "length_eq":
			return length == n, nil
		case "length_ne":
			return length != n, nil
		case "length_gt":
			return length > n, nil
		case "length_gte":
			return length >= n, nil
		case "length_lt":
			return length < n, nil
		default: // length_lte
			return length <= n, nil
		}
	}

	return false, fmt.Errorf("evalCondition: unknown operator %q", c.Op)
}
