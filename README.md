# agent-runtime

A runtime for agent workflows defined as declarative files. Agentic logic lives outside application code — flows are data, not code.

## Concept

A workflow is a directed graph of **nodes** (prompt calls, tool calls, maps, routers, parallel branches, subflows). You declare the graph in JSON files, the runtime executes it. No code required to author or modify a workflow.

Workflows are packaged as **bundles** — plain directories with a fixed structure. Every node and flow is versioned. Every reference is pinned. A bundle is fully reproducible.

## Install

```sh
go install github.com/levelplaneai/agent-runtime/cmd/agent-runtime@latest
```

## Usage

```sh
# Validate a bundle (checks structure, references, and all 9 validation rules)
agent-runtime validate ./my_agent.agent

# Run a bundle
agent-runtime run ./my_agent.agent --input topic="autumn rain"

# Pass a file as input (available to prompt nodes as multimodal content)
agent-runtime run ./my_agent.agent --input document=@./report.pdf

# Write a JSON trace to file
agent-runtime run ./my_agent.agent --input topic="fog" --trace trace.jsonl

# Register an HTTP tool
agent-runtime run ./my_agent.agent --tool supplier_api.get_price@v1=https://api.example.com/price

# Register a stub tool (for testing)
agent-runtime run ./my_agent.agent --tool supplier_api.get_price@v1='{"price": 42.0}'

# Run only a subset of the flow
agent-runtime run ./my_agent.agent --from extract_node --to summarize_node

# Pre-seed a node's output and skip re-running it
agent-runtime run ./my_agent.agent --seed outputs.json

# Save a checkpoint snapshot after every node (atomic write)
agent-runtime run ./my_agent.agent --checkpoint /tmp/run.snap

# Resume from a checkpoint (picks up where the run left off)
agent-runtime run ./my_agent.agent --resume /tmp/run.snap
```

## Checkpoint & resume

Any run can be made resumable by adding `--checkpoint <file>`. After each node completes the runtime atomically writes a JSON snapshot to that file. If the run is interrupted — crash, timeout, manual cancellation — call the same command with `--resume <snapshot>` instead of `--input` flags and execution picks up from the last saved node.

For agentic `prompt` nodes the checkpoint also captures mid-loop state (the full message history and current tool-use iteration), so an interrupted tool-use loop resumes from the right iteration rather than re-running from the beginning.

```sh
# Long-running flow; save state after each step
agent-runtime run ./pipeline.agent --input query="…" --checkpoint /tmp/pipeline.snap

# Interrupted? Resume without re-running completed nodes
agent-runtime run ./pipeline.agent --resume /tmp/pipeline.snap

# Resume and stop early
agent-runtime run ./pipeline.agent --resume /tmp/pipeline.snap --to review_node
```

`--resume` cannot be combined with `--input`, `--from`, or `--seed` (those values are stored inside the snapshot).

## Environment variables

The runtime checks for API keys at startup and uses whichever providers are configured:

| Variable | Provider |
|----------|----------|
| `ANTHROPIC_API_KEY` | Anthropic (Claude) |
| `OPENAI_API_KEY` | OpenAI |
| `GEMINI_API_KEY` | Google Gemini |

## Authoring flows

See **[FLOWS.md](FLOWS.md)** for the complete authoring reference — bundle structure, all file formats, all six node types, and the validation checklist.

The [testdata/](testdata/) directory has three working examples:

- `haiku_maker.agent` — two-node linear flow (prompt → prompt)
- `doc_summary.agent` — single-node flow, file input
- `rfq_processor.agent` — map, router, tool_call, multi-version nodes

## Bundle structure

```
my_agent.agent/
├── manifest.json       ← entry flow, tools required
├── flows/main/v1/
│   └── flow.json       ← node graph and edges
├── nodes/<name>/v1/
│   └── node.json       ← one node definition per directory
└── tools/<name>/v1/
    └── signature.json  ← tool contract (for validation)
```

## Node types

| Type | What it does |
|------|-------------|
| `prompt` | LLM call with templated messages |
| `tool_call` | Deterministic tool invocation |
| `map` | Fan-out over an array; runs one node per item |
| `router` | Conditional branching (rule-based or LLM-based) |
| `parallel` | Run named branches concurrently, merge outputs |
| `subflow` | Call another flow as a node |

