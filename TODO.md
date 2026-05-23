# TODO

- [x] Define Go types for manifest, flow, node, tool signature
- [x] Implement bundle directory loader (reads files into types)
- [x] Validate manifest.json fields and `entry` format (`name@version`)
- [x] Validate all `nodes` in a flow exist as versioned directories
- [x] Validate all `edges` reference nodes declared in the flow's `nodes` map
- [x] Validate all `from` paths reference nodes that exist in the flow
- [x] Validate all tools in nodes are declared in `manifest.tools_required`
- [x] Validate no cycles in the static edge graph
- [x] Wire up `agent-runtime validate <path>` CLI command
- [x] Write a sample bundle to test validation against

## Phase 2: Runtime executor — `prompt` and `tool_call`, linear flows

- [x] Define `Tool` interface and `ToolRegistry` (register by `name@version`, look up at execution time)
- [x] Define execution context type: holds flow inputs, per-node outputs, current node state
- [x] Implement `from` JSONPath resolver (`$.inputs.*`, `$.<node>.output`, `$.<node>.output.<path>`)
- [x] Implement prompt template engine: `{{ name }}` substitution against declared inputs
- [x] Implement `tool_call` node executor: resolves inputs, looks up tool in registry, calls it, stores output
- [x] Implement `prompt` node executor: resolves inputs, renders template (inline `system`/`user` or `system.prompt`/`user.prompt` files), calls Anthropic API, validates output against `output_schema`
- [x] Implement `on_error` handling: `fail`, `skip`, and `retry:N` with per-node retry loop
- [x] Implement linear flow executor: follows `edges` from `entry` node, threads execution context through each step
- [x] Wire up `agent-runtime run <bundle-path> [--input key=value...]` CLI command
- [ ] Extend testdata bundle with a `tool_call` node and a `prompt` node that uses `config.tools`, to exercise both executors end-to-end
