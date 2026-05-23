# The Agent Runtime Project

## Overview

Agent Runtime is a declarative workflow engine that executes agentic pipelines defined as
plain files. Workflows are expressed as directed graphs of nodes — each node is a discrete
unit of work such as an LLM prompt, a tool call, or a control-flow primitive like a router
or map.

## Design Goals

- **Reproducibility.** Every entity (flow, node, tool) is versioned with an explicit integer
  version. All cross-references pin both name and version. Running the same bundle twice
  always produces identical behaviour.

- **Portability.** A bundle is a directory that can be zipped, shipped, and executed anywhere
  the runtime is installed. No external registry or database is required.

- **LLM-editability.** The format is optimised for LLMs to read, author, and patch. Edits
  are surgical file operations: create a new version directory, update the reference, optionally
  promote the entry point.

## Node Primitives

The runtime supports six node types:

1. **prompt** — LLM call with templated messages and optional structured output schema.
2. **tool_call** — Deterministic invocation of a registered tool (API, database, etc.).
3. **map** — Fan-out over an array; runs the same node once per item with configurable concurrency.
4. **router** — Conditional branching, either rule-based (JSONPath expressions) or LLM-driven.
5. **parallel** — Fixed set of named branches executed concurrently; outputs merged into an object.
6. **subflow** — Calls another flow as a single composable node.

## Current Status

The v0.1 runtime implements all six node types. Provider support covers Anthropic, OpenAI, and
Google Gemini. File inputs (PDFs, images, and plain-text documents) are now supported via
inline base64 encoding; all three providers handle image blocks while Anthropic additionally
handles PDF and text documents natively.
