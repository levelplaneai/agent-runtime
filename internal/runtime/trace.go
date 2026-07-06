package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

// TraceEvent is one structured event emitted during a flow execution.
// The zero value of unused fields is omitted from JSON output.
type TraceEvent struct {
	Event        string         `json:"event"`
	Flow         string         `json:"flow,omitempty"`
	Bundle       string         `json:"bundle,omitempty"`
	Node         string         `json:"node,omitempty"`
	NodeType     string         `json:"type,omitempty"`
	Tool         string         `json:"tool,omitempty"`
	Model        string         `json:"model,omitempty"`
	Inputs       map[string]any `json:"inputs,omitempty"`
	Output       map[string]any `json:"output,omitempty"`
	Args         map[string]any `json:"args,omitempty"`
	Error        string         `json:"error,omitempty"`
	Attempt      int            `json:"attempt,omitempty"`
	MaxRetries   int            `json:"max_retries,omitempty"`
	InputTokens  int64          `json:"input_tokens,omitempty"`
	OutputTokens int64          `json:"output_tokens,omitempty"`
	DurationMS   int64          `json:"duration_ms,omitempty"`
	ItemIndex    int            `json:"item_index,omitempty"`
	ItemCount    int            `json:"item_count,omitempty"`
	Condition    string         `json:"condition,omitempty"`
	ChosenTarget string         `json:"chosen_target,omitempty"`
	BranchName   string         `json:"branch_name,omitempty"`
	RunID        string         `json:"run_id,omitempty"`
	TS           int64          `json:"ts"`
}

// Tracer writes structured trace events to a JSON writer and/or a human-readable
// writer. A nil *Tracer is valid and silently drops all events.
type Tracer struct {
	mu     sync.Mutex
	jsonW  io.Writer
	humanW io.Writer
}

// NewTracer creates a Tracer. Pass nil for either writer to disable that output.
func NewTracer(jsonW, humanW io.Writer) *Tracer {
	return &Tracer{jsonW: jsonW, humanW: humanW}
}

type ctxTracerKey struct{}

// ContextWithTracer returns a new context carrying t.
func ContextWithTracer(ctx context.Context, t *Tracer) context.Context {
	return context.WithValue(ctx, ctxTracerKey{}, t)
}

// tracerFrom extracts the Tracer from ctx. Returns nil if not set.
func tracerFrom(ctx context.Context) *Tracer {
	t, _ := ctx.Value(ctxTracerKey{}).(*Tracer)
	return t
}

// Emit records a trace event. Safe to call on a nil *Tracer and from multiple goroutines.
func (t *Tracer) Emit(e TraceEvent) {
	if t == nil {
		return
	}
	e.TS = time.Now().UnixMilli()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.jsonW != nil {
		b, _ := json.Marshal(e)
		fmt.Fprintf(t.jsonW, "%s\n", b)
	}
	if t.humanW != nil {
		t.writeHuman(e)
	}
}

func (t *Tracer) writeHuman(e TraceEvent) {
	w := t.humanW
	switch e.Event {
	case "flow_start":
		fmt.Fprintf(w, "[FLOW]  start   %-24s entry=%s\n", e.Bundle, e.Flow)
	case "flow_done":
		fmt.Fprintf(w, "[FLOW]  done    %-24s duration=%dms\n", e.Bundle, e.DurationMS)
	case "flow_error":
		fmt.Fprintf(w, "[FLOW]  error   %-24s %s\n", e.Bundle, e.Error)
	case "node_start":
		fmt.Fprintf(w, "[NODE]  start   %-24s type=%s\n", e.Node, e.NodeType)
	case "node_done":
		fmt.Fprintf(w, "[NODE]  done    %-24s duration=%dms\n", e.Node, e.DurationMS)
	case "node_skip":
		fmt.Fprintf(w, "[NODE]  skip    %-24s %s\n", e.Node, truncate(e.Error, 80))
	case "node_retry":
		fmt.Fprintf(w, "[NODE]  retry   %-24s attempt=%d/%d %s\n", e.Node, e.Attempt, e.MaxRetries, truncate(e.Error, 60))
	case "node_error":
		fmt.Fprintf(w, "[NODE]  error   %-24s %s\n", e.Node, e.Error)
	case "llm_request":
		fmt.Fprintf(w, "[LLM]   →       %-24s model=%s\n", e.Node, e.Model)
	case "llm_response":
		fmt.Fprintf(w, "[LLM]   ←       %-24s model=%s in=%d out=%d duration=%dms\n",
			e.Node, e.Model, e.InputTokens, e.OutputTokens, e.DurationMS)
	case "agent_iteration":
		fmt.Fprintf(w, "[AGENT] iter    %-24s iteration=%d\n", e.Node, e.Attempt)
	case "tool_start":
		if e.Attempt > 0 {
			fmt.Fprintf(w, "[TOOL]  → [%d]  %-24s tool=%s\n", e.Attempt, e.Node, e.Tool)
		} else {
			fmt.Fprintf(w, "[TOOL]  →       %-24s tool=%s\n", e.Node, e.Tool)
		}
	case "tool_done":
		if e.Attempt > 0 {
			fmt.Fprintf(w, "[TOOL]  ← [%d]  %-24s tool=%s duration=%dms\n", e.Attempt, e.Node, e.Tool, e.DurationMS)
		} else {
			fmt.Fprintf(w, "[TOOL]  ←       %-24s tool=%s duration=%dms\n", e.Node, e.Tool, e.DurationMS)
		}
	case "tool_error":
		if e.Attempt > 0 {
			fmt.Fprintf(w, "[TOOL]  err[%d]  %-24s tool=%s %s\n", e.Attempt, e.Node, e.Tool, truncate(e.Error, 60))
		} else {
			fmt.Fprintf(w, "[TOOL]  err     %-24s tool=%s %s\n", e.Node, e.Tool, truncate(e.Error, 60))
		}
	case "builtin_tool_used":
		if e.Attempt > 0 {
			fmt.Fprintf(w, "[TOOL]  ↩ [%d]  %-24s builtin=%s\n", e.Attempt, e.Node, e.Tool)
		} else {
			fmt.Fprintf(w, "[TOOL]  ↩       %-24s builtin=%s\n", e.Node, e.Tool)
		}
	case "map_start":
		fmt.Fprintf(w, "[MAP]   start   %-24s items=%d\n", e.Node, e.ItemCount)
	case "map_item_done":
		fmt.Fprintf(w, "[MAP]   item    %-24s %d/%d\n", e.Node, e.ItemIndex, e.ItemCount)
	case "loop_start":
		fmt.Fprintf(w, "[LOOP]  start   %-24s items=%d\n", e.Node, e.ItemCount)
	case "loop_item_done":
		fmt.Fprintf(w, "[LOOP]  item    %-24s %d/%d\n", e.Node, e.ItemIndex, e.ItemCount)
	case "loop_queue_extended":
		fmt.Fprintf(w, "[LOOP]  extend  %-24s queue_len=%d\n", e.Node, e.ItemCount)
	case "parallel_start":
		fmt.Fprintf(w, "[PAR]   start   %-24s branches=%d\n", e.Node, e.ItemCount)
	case "parallel_branch_done":
		fmt.Fprintf(w, "[PAR]   branch  %-24s %s done\n", e.Node, e.BranchName)
	case "subflow_start":
		fmt.Fprintf(w, "[SUB]   start   %-24s flow=%s\n", e.Node, e.Flow)
	case "subflow_done":
		fmt.Fprintf(w, "[SUB]   done    %-24s flow=%s duration=%dms\n", e.Node, e.Flow, e.DurationMS)
	case "router_branch":
		if e.Condition == "" {
			fmt.Fprintf(w, "[ROUTE] branch  %-24s default → %s\n", e.Node, e.ChosenTarget)
		} else {
			fmt.Fprintf(w, "[ROUTE] branch  %-24s when=%q → %s\n", e.Node, e.Condition, e.ChosenTarget)
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
