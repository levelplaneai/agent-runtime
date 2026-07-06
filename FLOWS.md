# Flow Authoring Reference

A **bundle** is a directory of JSON files that declares a workflow. The runtime executes it; you never write code.

## Directory layout

```
my_agent.agent/
├── manifest.json
├── flows/
│   └── main/
│       └── v1/
│           └── flow.json
├── nodes/
│   ├── step_one/
│   │   └── v1/
│   │       └── node.json
│   └── step_two/
│       └── v1/
│           └── node.json
└── tools/                          # only needed if you use tool_call nodes
    └── my_tool.do_thing/
        └── v1/
            └── signature.json
```

Rules: directory name = identity, no `id` fields anywhere. Every reference uses `name@version`.

---

## `manifest.json`

```json
{
  "bundle_version": "1.0.0",
  "runtime_version": "0.1",
  "name": "my_agent",
  "description": "What this bundle does",
  "entry": "main@v1",
  "tools_required": []
}
```

| Field | Required | Notes |
|-------|----------|-------|
| `runtime_version` | yes | always `"0.1"` |
| `entry` | yes | `<flow_name>@<version>` — what runs when the bundle is invoked |
| `tools_required` | yes | list every tool referenced anywhere in nodes, or `[]` |

---

## `flow.json`

```json
{
  "description": "Generate a haiku then critique it",
  "inputs": {
    "topic": { "type": "string" }
  },
  "outputs": {
    "haiku":   { "from": "$.make_haiku.output" },
    "critique": { "from": "$.critique_haiku.output" }
  },
  "entry": "make_haiku",
  "nodes": {
    "make_haiku":    "make_haiku@v1",
    "critique_haiku": "critique_haiku@v1"
  },
  "edges": [
    { "from": "make_haiku", "to": "critique_haiku" }
  ]
}
```

| Field | Required | Notes |
|-------|----------|-------|
| `description` | yes | used by LLM editors |
| `entry` | yes | local name of first node |
| `nodes` | yes | `local_name → name@version`; local name used in edges/goto/do |
| `edges` | yes | sequential transitions; can be `[]` for single-node flows |
| `inputs` / `outputs` | yes | omitting `outputs` silently returns `{}`; omitting `inputs` leaves the contract undeclared |

---

## Node envelope (all types)

```json
{
  "type": "...",
  "description": "What this node does",
  "inputs": {
    "my_input": { "from": "$.path.to.value" },
    "my_image": { "from": "$.crop_step.output.path", "type": "file_path" }
  },
  "config": { },
  "output_schema": { },
  "on_error": "retry:2"
}
```

`on_error`: `fail` | `skip` | `retry:N` (default `retry:2`)

### Input binding types

| `type` value | Behaviour |
|---|---|
| _(omitted)_ | Value is passed through as-is (string, number, object, array) |
| `"file_path"` | Resolved value must be a string path; runtime reads the file and wraps it in a `FileValue` (binary + MIME type). On `prompt` nodes the file is sent as a multimodal image or document block. |
| `"file_path_array"` | Resolved value must be an array of string paths; each is loaded into a `FileValue` the same way as `"file_path"`, producing `[]FileValue`. On `prompt` nodes each element is sent as its own multimodal content block. |

`file_path` is useful when an earlier `tool_call` node produces a file (e.g. a cropped image) and a downstream `prompt` node needs to send it to the model as multimodal content. The MIME type is detected automatically from the file extension (`.png` → `image/png`, `.pdf` → `application/pdf`, etc.). A missing or unreadable file surfaces as a node error and is subject to `on_error`.

`file_path_array` is the same idea for a list of files, e.g. a set of cropped views produced by an earlier `map` or `tool_call` node.

---

## Node types

### `prompt` — LLM call

```json
{
  "type": "prompt",
  "description": "Write a haiku about the given topic",
  "inputs": { "topic": { "from": "$.inputs.topic" } },
  "config": {
    "model": "anthropic/claude-haiku-4-5-20251001",
    "system": "You are a haiku poet.",
    "user": "Write a haiku about: {{ topic }}",
    "temperature": 0.7
  },
  "output_schema": {
    "type": "object",
    "properties": {
      "lines": { "type": "array", "items": { "type": "string" } }
    },
    "required": ["lines"]
  }
}
```

- `{{ name }}` substitutes a declared input. Undeclared = validation error.
- Omit `system`/`user` to load `system.prompt`/`user.prompt` files from the same directory.
- Add `"tools": ["tool_name@v1"]` and optional `"max_tool_iterations": 10` to let the LLM call tools.
- To pass a file as multimodal content (image or document), declare the input with `"type": "file_path"`. The runtime reads the file and appends it as a content block after the user message text. Works with any `FileValue` supplied at flow startup (via `--input key=@path`) or with a path string produced by an earlier `tool_call` node using `"type": "file_path"`.
- `"max_tokens"` (optional, default `16000`) caps the model's response length. Must be a positive integer; malformed or non-positive values are a validation error rather than a silent fallback.
- `"temperature"` (optional) must be a number when present; malformed values are a validation error.

### Multi-turn messages (`config.messages`)

Instead of a single `system`/`user` pair, a prompt node can declare an explicit turn sequence:

```json
{
  "config": {
    "model": "anthropic/claude-haiku-4-5-20251001",
    "messages": [
      { "role": "user", "content": "Here is the original diagram:" },
      { "role": "user", "content": [
        { "type": "text", "value": "Original Diagram:" },
        { "type": "image", "input": "drawing" }
      ]},
      { "role": "assistant", "content": "Understood." },
      { "role": "user", "content": [
        { "type": "image_sequence", "input": "crops", "label": "View: {{ name }}", "image_key": "path" }
      ]}
    ]
  }
}
```

Each turn's `content` is either:
- a plain string — rendered with `{{ name }}` templates, same as `config.user`.
- an array of content items, for interleaving text and images within one turn:
  - `{"type": "text", "value": "..."}` — rendered text block.
  - `{"type": "image", "input": "<name>"}` — `<name>` must resolve to a `FileValue` (an input bound with `"type": "file_path"`).
  - `{"type": "image_sequence", "input": "<name>", "label": "...", "image_key": "..."}` — `<name>` resolves to a `[]FileValue` (a `"file_path_array"` input) or a `[]any` of objects. Emits an optional rendered `label` followed by an image, per element. For `[]any` objects, `image_key` names the field holding the file path, and other object fields are exposed as `{{ field }}` in `label`.

Assistant turns are for supplying few-shot history — only plain text (string or a text-only content array) is guaranteed to work across all providers; image/document blocks on an `assistant` turn are rejected on OpenAI (the Chat Completions API has no multimodal assistant content).

---

### `tool_call` — deterministic tool invocation

```json
{
  "type": "tool_call",
  "description": "Fetch current price for a part",
  "inputs": { "part": { "from": "$.inputs.part_number" } },
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

Tool must be in `manifest.tools_required`.

---

### `map` — fan-out over an array

```json
{
  "type": "map",
  "description": "Price each line item",
  "config": {
    "over": "$.extract_items.output.items",
    "as": "item",
    "do": "price_one_item",
    "concurrency": 5
  },
  "output_schema": {
    "type": "array",
    "items": { "type": "object" }
  }
}
```

- `do` references a local node name from the flow's `nodes` map.
- Inside the `do` node, the item is available as `$.item` (or whatever `as` is set to).
- `concurrency`: integer or `"unlimited"` (default `1`).

---

### `loop` — sequential iteration with a dynamically growing queue

```json
{
  "type": "loop",
  "description": "Verify each feature, following up on features discovered mid-verification",
  "config": {
    "over": "$.phase4.output.names",
    "as": "feature_name",
    "do": "verify_feature",
    "append_from": "new_features",
    "accumulate": "verifications",
    "max_iterations": 200
  },
  "output_schema": {
    "type": "object",
    "properties": { "verifications": { "type": "array", "items": { "type": "object" } } }
  }
}
```

Unlike `map`, `loop` runs strictly sequentially and its queue can grow while it runs: after each iteration, if the `do` node's output has a non-empty array at the key named by `append_from`, those items are appended to the end of the queue and processed in turn. This models a feedback loop where each iteration can discover new work (e.g. verifying a feature surfaces a related feature that also needs verifying).

| Field | Required | Notes |
|---|---|---|
| `over` | yes | path to the initial `[]any` queue |
| `as` | yes | iteration variable name, accessible as `$.<as>` in the `do` node |
| `do` | yes | local node name to execute per item |
| `append_from` | no | key in the `do` node's output holding `[]any` of newly discovered items to enqueue |
| `accumulate` | no | key name for the collected results in the loop's output (default `"items"`) |
| `max_iterations` | no | hard cap on total items processed, default `1000`. Guards against a `do` node (typically an LLM) that keeps returning new items forever — the loop errors out once the cap is hit rather than growing unbounded |

---

### `router` — conditional branching

**Deterministic:**
```json
{
  "type": "router",
  "description": "Skip to end if no items found",
  "inputs": { "items": { "from": "$.extract_items.output.items" } },
  "config": {
    "branches": [
      { "when": { "field": "items", "op": "length_eq", "value": 0 }, "goto": "empty_handler" },
      { "default": true, "goto": "process_items" }
    ]
  }
}
```

Each `when` is a structured condition object with three fields:
- `field` — key in the node's resolved input map
- `op` — operator: `eq`, `ne`, `gt`, `gte`, `lt`, `lte`, `contains`, `in`, `exists`, `length_eq`, `length_ne`, `length_gt`, `length_gte`, `length_lt`, `length_lte`
- `value` — JSON value to compare against (omit for `exists`)

**LLM-based:**
```json
{
  "type": "router",
  "description": "Classify RFQ type and route",
  "inputs": { "rfq": { "from": "$.inputs.rfq_document" } },
  "config": {
    "decide_with": {
      "model": "anthropic/claude-haiku-4-5-20251001",
      "prompt": "./classify.md",
      "choices": ["standard", "custom", "unclear"]
    },
    "branches": [
      { "when": { "field": "decision", "op": "eq", "value": "standard" }, "goto": "standard_flow" },
      { "when": { "field": "decision", "op": "eq", "value": "custom" },   "goto": "custom_flow" },
      { "default": true,                                                    "goto": "request_clarification" }
    ]
  }
}
```

Branches evaluated in order; first match wins. Routers own their routing via `goto` — they do **not** appear in `edges`.

---

### `parallel` — concurrent named branches

```json
{
  "type": "parallel",
  "description": "Fetch history and specs at the same time",
  "config": {
    "branches": {
      "history": "fetch_supplier_history",
      "specs":   "fetch_eng_specs"
    }
  },
  "output_schema": {
    "type": "object",
    "properties": {
      "history": { "type": "object" },
      "specs":   { "type": "object" }
    }
  }
}
```

Output is an object keyed by branch name. First error cancels remaining branches.

---

### `subflow` — call another flow as a node

```json
{
  "type": "subflow",
  "description": "Run quote validation flow",
  "inputs": { "quote": { "from": "$.generate_quote.output" } },
  "config": { "flow": "quote_validation@v1" }
}
```

The target flow's `inputs`/`outputs` define the contract.

---

## State references

| Expression | Resolves to |
|------------|-------------|
| `$.inputs.<field>` | flow input |
| `$.<node_name>.output` | a node's full output |
| `$.<node_name>.output.<path>` | drill into a node's output |
| `$.<as_name>` | iteration variable inside a `map` or `loop` (the whole item) |
| `$.<as_name>.<field>` | field of an iteration variable (e.g. `$.region.path`) |
| `$.decision` | LLM router's chosen branch (inside router only) |

Used in: `inputs[*].from`, `outputs[*].from`, `router.branches[*].when`, `map.config.over`, `loop.config.over`.

Avoid naming a `map`/`loop` `as` value `inputs` — `$.inputs.<field>` is always resolved as a flow input first, so a nested path into an iteration variable named `inputs` is unreachable (only the bare `$.inputs`, with no further path, would resolve to the current item).

---

## Making changes (versioning model)

Edits are file operations. Old versions are never modified — always create a new version directory.

- **Edit a node** → create `nodes/<name>/v<N+1>/`, write the new `node.json`. Old version stays runnable.
- **Update a flow to use the new node** → edit `flow.json` in place (update the `nodes` map entry to point at `v<N+1>`), or create `flows/<name>/v<N+1>/flow.json` if you want to version the flow itself.
- **Go live** → update `manifest.entry` to point at the new flow version. This is the only place "promotion" happens.
- **Revert** → set `manifest.entry` back to a previous flow version.

A change is always a deliberate two- or three-step operation: create the new version, update references, optionally promote. No silent propagation.

---

## Partial execution

The CLI supports running only a slice of a flow and seeding node outputs from a file. These flags are useful for development, testing, and resuming interrupted runs.

| Flag | Purpose |
|------|---------|
| `--from <node>` | Start execution at this node instead of the flow entry |
| `--to <node>` | Stop after this node completes (skip the rest of the flow) |
| `--seed <file>` | Pre-populate node outputs from a JSON file before execution starts |
| `--checkpoint <file>` | Atomically write a snapshot after every node |
| `--resume <file>` | Resume a previously checkpointed run |

**`--seed` file format:**
```json
{
  "seed_outputs": {
    "fetch_data": { "result": "cached result" },
    "extract_items": { "items": ["a", "b"] }
  }
}
```

**Checkpoint & resume** — `--checkpoint` fires after each node boundary and, for agentic `prompt` nodes that use tools, after each tool-use iteration as well. The snapshot records full message history so a mid-loop interruption resumes at the right iteration without replaying earlier tool calls.

`--resume` is incompatible with `--input`, `--from`, and `--seed` (the snapshot already contains that state). `--to` is allowed with `--resume` to stop a resumed run early.

---

## Validation checklist

Before running, the runtime checks:

- [ ] Every reference uses `name@version` syntax (bare names fail)
- [ ] Every referenced version directory exists and contains valid files
- [ ] Every node in `edges`, `goto`, `do`, `parallel.branches` exists in the flow's `nodes` map
- [ ] Every `{{ name }}` in a prompt matches a declared `inputs` key
- [ ] Every `from` path resolves to a node that runs before the consumer
- [ ] `entry` node exists in the flow's `nodes` map
- [ ] `manifest.entry` references a valid flow version
- [ ] Every tool in `tool_call.config.tool` and `prompt.config.tools` is in `manifest.tools_required`
- [ ] No cycles in the static edge graph
