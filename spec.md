# Agent Runtime — v0.1 Spec

## Concept

A runtime that executes agent workflows defined in declarative files. Agentic logic lives outside application code; flows are data, not code. The format is optimized for LLMs to read, author, and patch surgically.

A workflow is a directed graph of nodes. Each node is a unit of work (prompt, tool call, control flow). Edges define sequencing. Routers and maps own their own control flow internally.

Workflows are distributed as **bundles** — directories (zippable as a single artifact) with a fixed structure. Every node, flow, and tool signature is versioned. Versions are explicit and mandatory in every reference, so a bundle is fully reproducible: reading it tells you exactly what will run.

## Design principles

- **Bundle as the unit.** A workflow is a directory of files, not a single document. Edits are surgical file operations.
- **Everything versioned, every reference pinned.** No silent behavior changes. No "current" pointers. Versioning is intrinsic to the format.
- **Three kinds of versioned entities only:** flows, nodes, tools. Keeps the mental model small.
- **Schemas are inline.** LLM output schemas are local to each node — they describe what *this* prompt is tuned to produce.
- **References by name + version, never by path.** Identity is logical; file layout is implementation.
- **Six node primitives.** Cover ~90% of workflows; escape hatch via `subflow`.
- **No implicit state.** Every node declares its inputs and outputs explicitly.

## Bundle structure

```
my_flow.agent/
├── manifest.json
├── flows/
│   ├── main/
│   │   ├── v1/flow.json
│   │   ├── v2/flow.json
│   │   └── v3/flow.json
│   └── quote_validation/
│       └── v1/flow.json
├── nodes/
│   ├── extract_items/
│   │   ├── v1/
│   │   │   ├── node.json
│   │   │   └── user.prompt
│   │   ├── v2/
│   │   │   ├── node.json
│   │   │   └── user.prompt
│   │   └── v3/
│   │       ├── node.json
│   │       └── user.prompt
│   ├── price_each_item/
│   │   └── v1/node.json
│   └── synthesize_quote/
│       └── v2/
│           ├── node.json
│           └── user.prompt
└── tools/
    └── supplier_api.get_price/
        └── v1/signature.json
```

Rules:

- Every node, flow, and tool lives in its own directory under `nodes/`, `flows/`, `tools/`.
- Each entity has version subdirectories: `v1/`, `v2/`, etc.
- Version subdirectories contain all files for that version (node definition, prompt, examples, tests).
- Directory name is the identity — no `id` field anywhere in files.
- The bundle is portable: zip the directory, ship it, unzip and run.

## `manifest.json`

The only place that knows about the bundle as a whole. Minimal.

```json
{
  "bundle_version": "1.0.0",
  "runtime_version": "0.1",
  "name": "rfq_processor",
  "description": "Processes manufacturing RFQs end-to-end",
  "entry": "main@v3",
  "tools_required": [
    "supplier_api.get_price@v1",
    "supplier_api.get_history@v1"
  ]
}
```

Fields:

- **`runtime_version`** — only declared here. Flows and nodes do not repeat it.
- **`entry`** — the entry flow with its version (`<flow_name>@<version>`). Single source of truth for "what runs when the bundle is invoked."
- **`tools_required`** — declares tools the host application must have registered. Validator checks signatures match.

## References

All cross-entity references use `<name>@<version>` syntax. The kind of entity is inferred from context.

| Where | Reference syntax | Kind |
|-------|------------------|------|
| `manifest.entry` | `main@v3` | flow |
| `flow.nodes` values | `extract_items@v3` | node |
| `subflow.config.flow` | `quote_validation@v1` | flow |
| `manifest.tools_required` | `supplier_api.get_price@v1` | tool |

Inside a node's version directory, references to local files (prompt.md, examples, etc.) use relative paths (`./prompt.md`). The `name@version` syntax is for bundle-level cross-references only.

## Flow file structure

```json
// flows/main/v3/flow.json
{
  "description": "Extracts line items from RFQ, prices each, generates quote",
  "inputs": {
    "rfq_document": { "type": "string" }
  },
  "outputs": {
    "final_quote": { "from": "$.synthesize_quote.output" }
  },
  "entry": "extract_items",
  "nodes": {
    "extract_items": "extract_items@v3",
    "check_completeness": "check_completeness@v1",
    "price_each_item": "price_each_item@v1",
    "price_one_item": "price_one_item@v2",
    "synthesize_quote": "synthesize_quote@v2"
  },
  "edges": [
    { "from": "extract_items", "to": "check_completeness" },
    { "from": "price_each_item", "to": "synthesize_quote" }
  ]
}
```

Key fields:

- **`inputs` / `outputs`** — declare the flow's contract. Makes it composable as a subflow.
- **`entry`** — local name of the first node to execute.
- **`nodes`** — map from local name → versioned node reference. The local name is used in edges, `goto`, `do`, and `parallel.branches`. This binding layer lets a flow use multiple versions of the same node under different local names if needed.
- **`edges`** — sequential transitions between nodes. Only place top-level sequencing lives. Routers don't appear here; they own their routing via `goto`.

## State references

Minimal JSONPath-like syntax for `from` and `when`:

- `$.inputs.<field>` — flow inputs
- `$.<local_node_name>.output` — another node's full output
- `$.<local_node_name>.output.<path>` — drill in
- `$.<as_name>` — iteration variable inside a `map` (lexically scoped)
- `$.decision` — router's decision result (only inside router branches)

Not Turing-complete. If you need logic, that's a `prompt` or `tool_call` node.

## Common node envelope

Every node file:

```json
{
  "type": "prompt | tool_call | map | router | parallel | subflow",
  "description": "What this node does (used by LLM editors)",
  "inputs": { "<name>": { "from": "$..." } },
  "output_schema": { /* inline JSON Schema */ },
  "config": { /* type-specific */ },
  "on_error": "fail | skip | retry:N"
}
```

The `description` field is required — it's how LLMs reason about a node without reading its prompt. Schemas are always inline.

## The six primitives

### 1. `prompt`

LLM call with templated messages.

```json
// nodes/extract_items/v3/node.json
{
  "type": "prompt",
  "description": "Parse RFQ text into structured list of line items",
  "inputs": {
    "rfq": { "from": "$.inputs.rfq_document" }
  },
  "config": {
    "model": "anthropic/claude-sonnet-4-5",
    "system": "You extract structured line items from RFQs.",
    "temperature": 0
  },
  "output_schema": {
    "type": "object",
    "properties": {
      "items": {
        "type": "array",
        "items": {
          "type": "object",
          "properties": {
            "part_number": { "type": "string" },
            "quantity": { "type": "integer" },
            "material": { "type": "string" }
          },
          "required": ["part_number", "quantity"]
        }
      }
    },
    "required": ["items"]
  }
}
```

**Templating rules:**

- `{{ name }}` substitutes a declared input. Undeclared references fail at validation, not runtime.
- `system` and `user` can be inline strings in config; if omitted the runtime loads `system.prompt` / `user.prompt` from the node version directory if present.
- For multi-turn, use `messages: [...]` instead of `system`/`user`.

**Generation parameters (all optional):**

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `max_tokens` | integer | 16000 | Maximum output tokens. |
| `temperature` | number | provider default | Sampling temperature. |
| `thinking_budget` | integer | — | Thinking token budget. `0` = disable thinking. Positive = budget in tokens. Supported by Gemini thinking models and Anthropic (minimum 1024 when enabled; must be less than `max_tokens`). |
| `reasoning_effort` | string | — | `"low"`, `"medium"`, or `"high"`. Supported by OpenAI o-series models and Gemini thinking models. Ignored for other providers. |

**Tools available during the prompt (optional):**

A `prompt` node can declare tools the LLM is allowed to call during inference. The runtime handles the tool-use loop internally — calls model, executes any tool calls, feeds results back, repeats until the model produces output matching `output_schema` or hits `max_tool_iterations`.

```json
{
  "type": "prompt",
  "description": "Research and summarize a supplier",
  "inputs": { "supplier_name": { "from": "$.inputs.supplier" } },
  "config": {
    "model": "anthropic/claude-sonnet-4-5",
    "system": "You research suppliers. Use tools when you need data.",
    "user": "Research the supplier: {{ supplier_name }}",
    "tools": [
      "supplier_api.get_history@v1",
      "supplier_api.get_certifications@v1",
      "web_search@v1"
    ],
    "max_tool_iterations": 10
  },
  "output_schema": {
    "type": "object",
    "properties": {
      "summary": { "type": "string" },
      "risk_score": { "type": "number" }
    }
  }
}
```

Tools referenced here must also be declared in `manifest.tools_required`. The LLM decides when (or whether) to call them. Default `max_tool_iterations` is 10.

### 2. `tool_call`

Deterministic tool invocation as an explicit pipeline step.

```json
{
  "type": "tool_call",
  "description": "Fetch current price for a part from supplier API",
  "inputs": { "part": { "from": "$.current.part_number" } },
  "config": {
    "tool": "supplier_api.get_price@v1",
    "args": { "part_number": "{{ part }}" }
  },
  "output_schema": {
    "type": "object",
    "properties": { "price": { "type": "number" } }
  }
}
```

Tools are code, registered with the runtime. The bundle references them by `name@version`. Signatures live in `tools/<name>/<version>/signature.json` for validation.

**When to use `tool_call` vs `prompt` with tools:**

| Use `tool_call` node when... | Use `prompt` with tools when... |
|------------------------------|----------------------------------|
| You know the tool must run at a specific point | The LLM should decide whether a tool is needed |
| No LLM reasoning is required (deterministic API/DB call) | Multiple tools may be needed based on intermediate results |
| You want each call as a distinct step in the trace | You want the LLM to drive open-ended exploration |
| You want one LLM call, one tool call, done | You're building a research/debugging/investigation agent |

Both can be used in the same flow. `tool_call` keeps the flow in control; `prompt` with tools delegates control to the LLM for that node.

### 3. `map`

Fan-out over an array; the `do` node runs per item.

```json
{
  "type": "map",
  "description": "Run pricing analysis on each line item in parallel",
  "config": {
    "over": "$.extract_items.output.items",
    "as": "item",
    "do": "price_one_item",
    "concurrency": 5
  },
  "output_schema": {
    "type": "array",
    "items": {
      "type": "object",
      "properties": {
        "part_number": { "type": "string" },
        "price": { "type": "number" },
        "cycle_time_days": { "type": "number" }
      }
    }
  }
}
```

- `over` — array to iterate
- `as` — variable name the item binds to inside the `do` node (available as `$.<as>`)
- `do` — local node name to execute per item
- `concurrency` — parallelism (default 1, or `"unlimited"`)

Map output is automatically an array of the `do` node's outputs. Maps nest cleanly: inner map sees outer map's `as` variable.

### 4. `router`

Conditional branching. Two flavors share one node type.

**Deterministic** (rule-based):

```json
{
  "type": "router",
  "description": "Route based on whether RFQ has all required fields",
  "inputs": { "items": { "from": "$.extract_items.output.items" } },
  "config": {
    "branches": [
      { "when": "$.inputs.items.length == 0", "goto": "request_clarification" },
      { "default": true, "goto": "price_each_item" }
    ]
  }
}
```

**LLM-based** (semantic):

```json
{
  "type": "router",
  "description": "Decide processing path based on RFQ type",
  "inputs": { "rfq": { "from": "$.inputs.rfq_document" } },
  "config": {
    "decide_with": {
      "model": "anthropic/claude-haiku-4-5",
      "prompt": "./classify.md",
      "choices": ["standard", "custom_machining", "assembly", "unclear"],
      "max_tokens": 8192,
      "thinking_budget": 0,
      "reasoning_effort": "low"
    },
    "branches": [
      { "when": "$.decision == 'standard'", "goto": "standard_flow" },
      { "when": "$.decision == 'custom_machining'", "goto": "machining_flow" },
      { "when": "$.decision == 'assembly'", "goto": "assembly_flow" },
      { "when": "$.decision == 'unclear'", "goto": "request_clarification" }
    ]
  }
}
```

**`decide_with` fields:**

| Key | Required | Default | Description |
|-----|----------|---------|-------------|
| `model` | yes | — | Provider/model string. |
| `prompt` | yes | — | Inline prompt string or `./relative` path to a `.md` file. |
| `choices` | yes | — | Array of strings the model must choose from. |
| `max_tokens` | no | 8192 | Maximum output tokens for the classification call. |
| `thinking_budget` | no | — | Same semantics as on `prompt` nodes. Useful for thinking models. |
| `reasoning_effort` | no | — | Same semantics as on `prompt` nodes. |

Routers transfer control via `goto`. They do **not** appear in the top-level `edges` array.

### 5. `parallel`

Run multiple named branches concurrently; outputs merged into an object.

```json
{
  "type": "parallel",
  "description": "Fetch supplier history and engineering specs in parallel",
  "config": {
    "branches": {
      "history": "fetch_supplier_history",
      "specs": "fetch_eng_specs"
    }
  },
  "output_schema": {
    "type": "object",
    "properties": {
      "history": { "type": "object" },
      "specs": { "type": "object" }
    }
  }
}
```

Differs from `map`: fixed set of named branches running different work, not the same work over a list.

### 6. `subflow`

Call another flow as a node. Escape hatch for composition and reuse.

```json
{
  "type": "subflow",
  "description": "Run quote validation flow",
  "inputs": { "quote": { "from": "$.generate_quote.output" } },
  "config": { "flow": "quote_validation@v1" }
}
```

Subflow's declared `inputs` / `outputs` define its contract.

## Error handling (v0.1)

Per-node `on_error` field, default `"retry:2"` then fail.

- `fail` — halt the flow
- `skip` — return null for this node, continue
- `retry:N` — retry up to N times, then fail

In a `map`, `skip` lets the array continue past failed items.

## Validation rules (enforced before execution and on every edit)

1. Every reference uses `<name>@<version>` syntax — bare names fail.
2. Every referenced version directory exists and contains valid files.
3. Every node referenced in `edges`, `goto`, `do`, or `parallel.branches` exists in the flow's `nodes` map.
4. Every `{{ name }}` in a prompt template matches a declared `inputs` key.
5. Every `from` JSONPath resolves to a node that exists and runs before the consumer.
6. `entry` node exists in the flow's `nodes` map.
7. `manifest.entry` references a valid flow version.
8. Every tool referenced — whether in a `tool_call` node's `config.tool` or in a `prompt` node's `config.tools` — is declared in `manifest.tools_required`.
9. No cycles in the static edge graph (loops happen via routers' `goto`, which is explicitly allowed).

Validation runs on every LLM edit and before any execution. Failures return precise error messages an LLM can act on.

## LLM editing model

Edits are file operations:

- **Edit a node** → create `nodes/<name>/v<N+1>/`, write `node.json` and associated files. Old version untouched and still runnable.
- **Adopt a new node version in a flow** → either edit the flow file in place (if the flow version stays the same), or create a new flow version pointing at the new node version.
- **Promote a new flow** → update `manifest.entry`. The only place "going live" happens.
- **Revert** → update `manifest.entry` back to a previous flow version. Done.
- **Compare** → diff two version directories.
- **Soft delete** → remove references; old versions stay as history. Garbage collect later if needed.

Because versions are mandatory and pinned, a "change" is always a deliberate two- or three-step operation: create the new version, update references, optionally promote. No silent propagation, ever.

## Out of scope for v0.1

Deferred to keep v0.1 shippable:

- Loops as a primitive (use router `goto`)
- Streaming outputs
- Human-in-the-loop pauses
- Cross-run memory / persistence
- Patch operation format (LLM emits file ops for now; structured patch ops can come later)
- A `transform` node for pure data shaping
- Shared schema definitions (inline only; revisit if reuse pain emerges)
- Named/semantic version labels (strict `v<N>` integers only)

## Build order

1. Bundle loader + validator (handles directory structure, version resolution, all validation rules)
2. Runtime executor for `prompt` and `tool_call` only (linear flows)
3. Add `map` and `parallel`
4. Add `router` (both flavors)
5. Add `subflow`
6. Tool registration API for host applications
7. Execution trace format (for observability and LLM-driven debugging)

## Open decisions to lock before building

- **Tool registration mechanics** — how host apps register tools, what the interface looks like, how signatures are matched
- **Execution trace format** — structured enough for LLMs to read and propose fixes; this is critical infrastructure
- **Bundle storage** — filesystem, DB, content-addressed store, or all of the above
- **Concurrency model** — thread pool, async, worker processes
- **Bundle distribution** — how bundles are shared, named, versioned externally (registry? plain zip files?)
