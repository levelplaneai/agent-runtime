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

`file_path` is useful when an earlier `tool_call` node produces a file (e.g. a cropped image) and a downstream `prompt` node needs to send it to the model as multimodal content. The MIME type is detected automatically from the file extension (`.png` → `image/png`, `.pdf` → `application/pdf`, etc.). A missing or unreadable file surfaces as a node error and is subject to `on_error`.

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

### `router` — conditional branching

**Deterministic:**
```json
{
  "type": "router",
  "description": "Skip to end if no items found",
  "inputs": { "items": { "from": "$.extract_items.output.items" } },
  "config": {
    "branches": [
      { "when": "$.inputs.items.length == 0", "goto": "empty_handler" },
      { "default": true, "goto": "process_items" }
    ]
  }
}
```

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
      { "when": "$.decision == 'standard'", "goto": "standard_flow" },
      { "when": "$.decision == 'custom'",   "goto": "custom_flow" },
      { "default": true,                     "goto": "request_clarification" }
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
| `$.<as_name>` | iteration variable inside a `map` (the whole item) |
| `$.<as_name>.<field>` | field of an iteration variable (e.g. `$.region.path`) |
| `$.decision` | LLM router's chosen branch (inside router only) |

Used in: `inputs[*].from`, `outputs[*].from`, `router.branches[*].when`, `map.config.over`.

---

## Making changes (versioning model)

Edits are file operations. Old versions are never modified — always create a new version directory.

- **Edit a node** → create `nodes/<name>/v<N+1>/`, write the new `node.json`. Old version stays runnable.
- **Update a flow to use the new node** → edit `flow.json` in place (update the `nodes` map entry to point at `v<N+1>`), or create `flows/<name>/v<N+1>/flow.json` if you want to version the flow itself.
- **Go live** → update `manifest.entry` to point at the new flow version. This is the only place "promotion" happens.
- **Revert** → set `manifest.entry` back to a previous flow version.

A change is always a deliberate two- or three-step operation: create the new version, update references, optionally promote. No silent propagation.

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
