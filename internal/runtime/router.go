package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// Branch is one conditional arm of a router node.
type Branch struct {
	When    string `json:"when"`
	Default bool   `json:"default"`
	Goto    string `json:"goto"`
}

// decideWith holds the parsed config for an LLM-based router.
type decideWith struct {
	Model   string   `json:"model"`
	Prompt  string   `json:"prompt"`
	Choices []string `json:"choices"`
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

	t := tracerFrom(ctx)
	for _, b := range branches {
		if b.Default {
			t.Emit(TraceEvent{Event: "router_branch", Node: localName, ChosenTarget: b.Goto})
			return map[string]any{"_goto": b.Goto}, nil
		}
		match, err := evalWhen(b.When, resolved)
		if err != nil {
			return nil, err
		}
		if match {
			t.Emit(TraceEvent{Event: "router_branch", Node: localName, Condition: b.When, ChosenTarget: b.Goto})
			return map[string]any{"_goto": b.Goto}, nil
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

	temp := 0.0
	req := CompletionRequest{
		Model:       dw.Model,
		MaxTokens:   256,
		Messages:    []Message{{Role: "user", Content: rendered}},
		Temperature: &temp,
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
	t.Emit(TraceEvent{Event: "llm_request", Node: localName, Model: req.Model})
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
	})

	var out struct {
		Decision string `json:"decision"`
	}
	if err := json.Unmarshal([]byte(resp.Content), &out); err != nil {
		return "", "", fmt.Errorf("parsing LLM response: %w", err)
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

func parseBranches(config map[string]json.RawMessage) ([]Branch, error) {
	raw, ok := config["branches"]
	if !ok {
		return nil, fmt.Errorf("router: config missing 'branches'")
	}
	var branches []Branch
	if err := json.Unmarshal(raw, &branches); err != nil {
		return nil, fmt.Errorf("router: invalid 'branches': %w", err)
	}
	return branches, nil
}

// evalWhen evaluates a when condition against a map of resolved node inputs.
//
// Supported patterns (v0.1):
//
//	"$.inputs.<field>.length == <int>"  — check array length
//	"$.<field> == '<string>'"           — check string equality
func evalWhen(when string, resolved map[string]any) (bool, error) {
	when = strings.TrimSpace(when)

	// Pattern: $.inputs.<field>.length == <int>
	if strings.HasPrefix(when, "$.inputs.") && strings.Contains(when, ".length ==") {
		withoutPrefix := strings.TrimPrefix(when, "$.inputs.")
		dotLength := strings.Index(withoutPrefix, ".length ==")
		if dotLength < 0 {
			return false, fmt.Errorf("evalWhen: malformed condition %q", when)
		}
		field := withoutPrefix[:dotLength]
		rhs := strings.TrimSpace(withoutPrefix[dotLength+len(".length =="):])
		n, err := strconv.Atoi(rhs)
		if err != nil {
			return false, fmt.Errorf("evalWhen: expected integer after 'length ==', got %q", rhs)
		}
		val, ok := resolved[field]
		if !ok {
			return false, fmt.Errorf("evalWhen: input field %q not found in resolved inputs", field)
		}
		arr, ok := val.([]any)
		if !ok {
			return false, fmt.Errorf("evalWhen: input %q is not an array", field)
		}
		return len(arr) == n, nil
	}

	// Pattern: $.<field> == '<string>'
	if strings.HasPrefix(when, "$.") && strings.Contains(when, " == '") {
		withoutDollar := strings.TrimPrefix(when, "$.")
		eqIdx := strings.Index(withoutDollar, " == '")
		if eqIdx < 0 {
			return false, fmt.Errorf("evalWhen: malformed condition %q", when)
		}
		field := withoutDollar[:eqIdx]
		rest := withoutDollar[eqIdx+len(" == '"):]
		if !strings.HasSuffix(rest, "'") {
			return false, fmt.Errorf("evalWhen: condition %q: missing closing single quote", when)
		}
		expected := rest[:len(rest)-1]
		val, ok := resolved[field]
		if !ok {
			return false, fmt.Errorf("evalWhen: field %q not found in resolved inputs", field)
		}
		s, ok := val.(string)
		if !ok {
			return false, fmt.Errorf("evalWhen: field %q is not a string", field)
		}
		return s == expected, nil
	}

	return false, fmt.Errorf("evalWhen: unsupported condition %q", when)
}
