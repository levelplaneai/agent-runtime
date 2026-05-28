# Checkpoint / Resume & Partial Execution — Implementation Plan

## Current state

- **Frontier-based execution**: `runFrontier()` (`internal/runtime/flow.go:180`) queues nodes and
  processes them one by one. All state lives in-memory in `ExecutionContext` — no serialization,
  no resume mechanism exists.
- **Tools are separate nodes** — `tool_call` nodes are first-class graph nodes, not embedded in
  prompt nodes. There is no agentic loop (LLM → tool → LLM → …) today.
- **No checkpoint mechanism** — the `Tracer` emits structured events but purely for observation;
  it is not used for resumption.

---

## Features

### 1. Partial flow execution (startAt / stopAfter + fixture seeds)

Run a sub-section of a flow for testing/debugging — specify a start node and an end node. Upstream
node outputs can be supplied via a fixture JSON file, or replayed from a saved checkpoint.

**Changes:**

- Add `RunFlowOptions` struct with fields:
  - `StartAt string` — local node name to begin execution from (defaults to `flow.Entry`)
  - `StopAfter string` — local node name to halt after (defaults to running to completion)
  - `SeedOutputs map[string]any` — pre-populated node outputs (fixture data)
- In `runFrontier`: prime the frontier with `StartAt` instead of `flow.Entry`; after a node
  completes, check if it matches `StopAfter` and return early if so
- Seed `execCtx.nodeOutputs` with `SeedOutputs` before starting
- CLI: `agent-runtime run --from <node> --to <node> --seed fixtures.json`

**Fixture file format:**
```json
{
  "seed_outputs": {
    "node_local_name": { ... }
  }
}
```

---

### 2. Between-node checkpoint & resume

Serialize the full execution state at node boundaries so a run can be paused and resumed.

**Snapshot structure:**
```go
type Snapshot struct {
    RunID         string         `json:"run_id"`
    Timestamp     time.Time      `json:"timestamp"`
    BundleVersion string         `json:"bundle_version"`
    FlowRef       string         `json:"flow_ref"`          // "name@version"
    Inputs        map[string]any `json:"inputs"`
    NodeOutputs   map[string]any `json:"node_outputs"`      // JSON-encoded; see FileValue note
    Visited       []string       `json:"visited"`
    Frontier      []string       `json:"frontier"`
    ActiveNode    *NodeSnapshot  `json:"active_node,omitempty"` // set when paused mid-node
}
```

**Changes:**

- Add `OnCheckpoint func(Snapshot) error` callback to `RunFlowOptions` — called after each node
  completes; host app decides where to persist (file, DB, etc.)
- `RunFlowResume(ctx, bundle, snapshot, registry, provider, opts)` — restores `ExecutionContext`
  from snapshot, reconstructs the `visited` map and frontier, re-enters `runFrontier`
- The `Tracer` must re-use the same `RunID` on resume so traces stitch together

**Implementation notes:**

- `FileValue` (produced by `file_path` input bindings, `internal/runtime/resolve.go`) is stored
  in `nodeOutputs` as a Go struct — needs a stable JSON representation before snapshotting
  (base64-encoded content, or a re-readable path reference)
- `map` / `parallel` nodes: suspension is only allowed at join boundaries (when the node fully
  completes), not mid-fanout — avoids race conditions with cloned contexts
- `retry:N` policy (`internal/runtime/onerror.go`): checkpoint captures the current attempt count
  so the retry budget is preserved on resume
- `BundleVersion` / `RuntimeVersion` from the manifest serve as compatibility keys — refuse to
  resume a snapshot against a mismatched bundle version

---

### 3. Agentic tool-use loop in prompt nodes

Add the ability for a prompt node to drive a tool-call loop (ReAct / OpenAI-style tool use): the
LLM emits tool calls, the runtime executes them, results are fed back, and the cycle repeats until
the LLM produces a final text answer.

**Changes to `LLMProvider` interface:**

```go
type ToolCall struct {
    ID        string         `json:"id"`
    Name      string         `json:"name"`
    Arguments map[string]any `json:"arguments"`
}

type ToolUseResponse struct {
    Text      string     // non-empty when LLM produced a final answer
    ToolCalls []ToolCall // non-empty when LLM wants to call tools
}

// New method on LLMProvider:
CompleteWithTools(ctx context.Context, req CompleteWithToolsRequest) (ToolUseResponse, error)
```

All providers (OpenAI, Anthropic, etc.) need to implement `CompleteWithTools`.

**Changes to prompt node config** (`internal/bundle/types.go`):

```yaml
config:
  tools:
    - "search@1.0"
    - "calculator@1.0"
  max_iterations: 10   # safety limit; defaults to a reasonable cap if omitted
```

**Changes to `ExecutePrompt`** (`internal/runtime/prompt.go`):

- If `config.tools` is set, resolve tool signatures from the registry and pass them to
  `CompleteWithTools` as the available tool schema
- Loop:
  1. Call `provider.CompleteWithTools(ctx, req)`
  2. If `response.ToolCalls` is non-empty: execute each tool via registry, append tool results as
     messages, increment iteration counter, loop
  3. If `response.Text` is non-empty: return as node output
  4. If `iteration >= max_iterations`: return error
- If `config.tools` is absent: fall through to the existing single-shot `Complete` path (no
  behaviour change for existing flows)

---

### 4. Mid-loop checkpoint for agentic prompt nodes

Completes the checkpoint story once Tier 3 exists.

**`NodeSnapshot` (used in `Snapshot.ActiveNode`):**

```go
type NodeSnapshot struct {
    NodeName  string    `json:"node_name"`
    NodeType  string    `json:"node_type"`
    Messages  []Message `json:"messages,omitempty"` // full conversation so far
    Iteration int       `json:"iteration,omitempty"`
}
```

- After each tool-call round, call `OnCheckpoint` with an updated snapshot that includes
  `ActiveNode` — the host app can persist and the run can be resumed mid-loop
- On resume, `ExecutePrompt` detects a non-nil `ActiveNode` snapshot for the current node,
  restores the messages array and iteration count, and continues from where it left off

---

## Implementation order

| Tier | Feature | Rationale |
|------|---------|-----------|
| 1 | Partial execution (startAt/stopAfter + fixtures) | Cheapest; immediately useful for debugging; exercises plumbing used by resume |
| 2 | Between-node checkpoint & resume | Builds directly on Tier 1's `SeedOutputs` / `RunFlowOptions` |
| 3 | Agentic tool-use loop in prompt nodes | New capability; well-contained in `ExecutePrompt` + `LLMProvider` |
| 4 | Mid-loop checkpoint | Completes the picture; only possible after Tier 3 |

---

## Files affected (by tier)

**Tier 1 & 2:**
- `internal/runtime/flow.go` — `runFrontier`, `RunFlow`, add `RunFlowResume`
- `internal/runtime/state.go` — add serialization methods on `ExecutionContext`
- `internal/bundle/types.go` — `FileValue` JSON encoding
- `cmd/` — CLI flags `--from`, `--to`, `--seed`, `--resume`
- New: `internal/runtime/snapshot.go` — `Snapshot`, `NodeSnapshot` types + marshal/unmarshal

**Tier 3 & 4:**
- `internal/runtime/provider.go` (or equivalent) — extend `LLMProvider` interface
- `internal/runtime/prompt.go` — agentic loop in `ExecutePrompt`
- `internal/bundle/types.go` — `tools`, `max_iterations` prompt config fields
- `internal/runtime/snapshot.go` — `NodeSnapshot.Messages` support
- Provider implementations (OpenAI, Anthropic adapters) — `CompleteWithTools`
- Python SDK — wire through new interface in subprocess protocol
