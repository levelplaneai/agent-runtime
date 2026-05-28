package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/openai/openai-go"
	"google.golang.org/genai"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
	"github.com/levelplaneai/agent-runtime/internal/runtime"
)

// version is set at build time via -ldflags="-X main.version=v0.1.0".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "validate":
		cmdValidate(os.Args[2:])
	case "run":
		cmdRun(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println(version)
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage:")
	fmt.Fprintln(os.Stderr, "  agent-runtime validate <bundle-path>")
	fmt.Fprintln(os.Stderr, "  agent-runtime run <bundle-path> [flags]")
	fmt.Fprintln(os.Stderr, "  agent-runtime version")
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "flags:")
	fmt.Fprintln(os.Stderr, "  --input key=value                               pass a flow input (repeatable)")
	fmt.Fprintln(os.Stderr, "  --input key=@path/to/file.pdf                   pass a file as a flow input")
	fmt.Fprintln(os.Stderr, "  --trace <file>                                  write JSON trace events to file")
	fmt.Fprintln(os.Stderr, `  --tool name@version=https://host/path           register an HTTP tool (POST args as JSON to URL)`)
	fmt.Fprintln(os.Stderr, `  --tool name@version='{"key":"value"}'           register a stub tool with a fixed JSON response`)
	fmt.Fprintln(os.Stderr, `  --tool name@version                             register a stub tool that returns {}`)
	fmt.Fprintln(os.Stderr, "  --data-dir <path>                               SDK mode: stream events to stdout and persist run to <path>/runs/<run-id>/")
	fmt.Fprintln(os.Stderr, "  --run-id <id>                                   run identifier (required with --data-dir)")
	fmt.Fprintln(os.Stderr, "  --from <node>                                   start execution from this node (partial run)")
	fmt.Fprintln(os.Stderr, "  --to <node>                                     stop execution after this node (partial run)")
	fmt.Fprintln(os.Stderr, "  --seed <file>                                   JSON file with pre-seeded node outputs ({\"seed_outputs\":{...}})")
}

func cmdValidate(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: agent-runtime validate <bundle-path>")
		os.Exit(1)
	}

	b, err := bundle.Load(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading bundle: %v\n", err)
		os.Exit(1)
	}

	errs := bundle.Validate(b)
	if len(errs) == 0 {
		fmt.Printf("bundle %q is valid\n", b.Manifest.Name)
		return
	}

	fmt.Fprintf(os.Stderr, "%d validation error(s):\n", len(errs))
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "  - %v\n", e)
	}
	os.Exit(1)
}

func cmdRun(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: agent-runtime run <bundle-path> [--input key=value ...] [--trace <file>] [--tool name@version=https://... ...]")
		os.Exit(1)
	}

	bundlePath := args[0]
	flags, err := parseRunFlags(args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error parsing flags: %v\n", err)
		os.Exit(1)
	}

	b, err := bundle.Load(bundlePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error loading bundle: %v\n", err)
		os.Exit(1)
	}

	if errs := bundle.Validate(b); len(errs) > 0 {
		fmt.Fprintf(os.Stderr, "%d validation error(s):\n", len(errs))
		for _, e := range errs {
			fmt.Fprintf(os.Stderr, "  - %v\n", e)
		}
		os.Exit(1)
	}

	reg := runtime.NewRegistry()
	for ref, tool := range flags.tools {
		reg.Register(ref, bundle.ToolSignature{}, tool)
	}

	if missing := reg.MissingTools(b); len(missing) > 0 {
		fmt.Fprintf(os.Stderr, "error: %d required tool(s) not registered:\n", len(missing))
		for _, t := range missing {
			fmt.Fprintf(os.Stderr, "  - %s\n", t)
		}
		fmt.Fprintln(os.Stderr, `hint: use --tool name@version='{"key":"value"}' for a stub or --tool name@version=https://... for an HTTP service`)
		os.Exit(1)
	}

	// Validate --from / --to node names against the entry flow before execution.
	if flags.startAt != "" || flags.stopAfter != "" {
		flowName, flowVersion, ok := bundle.ParseRef(b.Manifest.Entry)
		if !ok {
			fmt.Fprintf(os.Stderr, "error: manifest.entry %q: invalid name@version format\n", b.Manifest.Entry)
			os.Exit(1)
		}
		entryFlow, ok := b.Flows[flowName][flowVersion]
		if !ok {
			fmt.Fprintf(os.Stderr, "error: entry flow %q not found in bundle\n", b.Manifest.Entry)
			os.Exit(1)
		}
		if flags.startAt != "" {
			if _, ok := entryFlow.Nodes[flags.startAt]; !ok {
				fmt.Fprintf(os.Stderr, "error: --from %q: node not found in flow %q\n", flags.startAt, b.Manifest.Entry)
				os.Exit(1)
			}
		}
		if flags.stopAfter != "" {
			if _, ok := entryFlow.Nodes[flags.stopAfter]; !ok {
				fmt.Fprintf(os.Stderr, "error: --to %q: node not found in flow %q\n", flags.stopAfter, b.Manifest.Entry)
				os.Exit(1)
			}
		}
	}

	// Pre-flight: check for missing API keys before creating the run directory so that
	// the SDK sees a clean non-zero exit (no partial meta.json) and can detect the error.
	if missingKeys := collectMissingKeys(b); len(missingKeys) > 0 {
		fmt.Fprintf(os.Stderr, "error: missing API key(s) required to run bundle %q\n\n", b.Manifest.Name)
		for _, m := range missingKeys {
			fmt.Fprintf(os.Stderr, "  %s\n", m.envVar)
			fmt.Fprintf(os.Stderr, "    models : %s\n", strings.Join(m.models, ", "))
			fmt.Fprintf(os.Stderr, "    nodes  : %s\n", strings.Join(m.nodes, ", "))
			fmt.Fprintf(os.Stderr, "    fix    : export %s=<your-key>\n\n", m.envVar)
			// Machine-readable marker parsed by the Python SDK.
			fmt.Fprintf(os.Stderr, "missing-api-key: %s\n", m.envVar)
		}
		os.Exit(1)
	}

	// SDK mode: create the run directory and write the initial meta before execution.
	var runDir string
	startedAt := time.Now().UnixMilli()
	if flags.dataDir != "" {
		if flags.runID == "" {
			fmt.Fprintln(os.Stderr, "error: --run-id is required when --data-dir is set")
			os.Exit(1)
		}
		runDir = filepath.Join(flags.dataDir, "runs", flags.runID)
		if err := os.MkdirAll(runDir, 0755); err != nil {
			fmt.Fprintf(os.Stderr, "error creating run directory: %v\n", err)
			os.Exit(1)
		}
		if err := writeMeta(runDir, runMeta{
			RunID:     flags.runID,
			Bundle:    bundlePath,
			StartedAt: startedAt,
			Status:    "running",
		}); err != nil {
			fmt.Fprintf(os.Stderr, "error writing meta: %v\n", err)
			os.Exit(1)
		}
	}

	// Build tracer.
	// SDK mode:  events → stdout + trace.jsonl (io.MultiWriter)
	// CLI mode:  events → --trace file (if specified)
	var traceW io.Writer
	var traceCloser io.Closer
	if flags.dataDir != "" {
		f, err := os.OpenFile(filepath.Join(runDir, "trace.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error creating trace file: %v\n", err)
			os.Exit(1)
		}
		traceW = io.MultiWriter(os.Stdout, f)
		traceCloser = f
	} else if flags.tracePath != "" {
		f, err := os.Create(flags.tracePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error creating trace file: %v\n", err)
			os.Exit(1)
		}
		traceW = f
		traceCloser = f
	}
	if traceCloser != nil {
		defer traceCloser.Close()
	}

	tracer := runtime.NewTracer(traceW, os.Stderr)
	ctx := runtime.ContextWithTracer(context.Background(), tracer)

	provider := buildProviderRegistry()

	// --resume: validate that conflicting flags are not set.
	if flags.resumePath != "" {
		if len(flags.inputs) > 0 || flags.startAt != "" || flags.seedPath != "" {
			fmt.Fprintln(os.Stderr, "error: --resume cannot be combined with --input, --from, or --seed (state is in the snapshot)")
			os.Exit(1)
		}
		if flags.dataDir != "" && flags.runID != "" {
			// runID will be validated against the snapshot below.
		}
	}

	var runOpts *runtime.RunFlowOptions
	if flags.startAt != "" || flags.stopAfter != "" || flags.seedPath != "" || flags.checkpointPath != "" {
		var seedOutputs map[string]any
		if flags.seedPath != "" {
			data, err := os.ReadFile(flags.seedPath)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error reading seed file: %v\n", err)
				os.Exit(1)
			}
			var seedFile struct {
				SeedOutputs map[string]any `json:"seed_outputs"`
			}
			if err := json.Unmarshal(data, &seedFile); err != nil {
				fmt.Fprintf(os.Stderr, "error parsing seed file %q: %v\n", flags.seedPath, err)
				os.Exit(1)
			}
			seedOutputs = seedFile.SeedOutputs
		}
		runOpts = &runtime.RunFlowOptions{
			StartAt:     flags.startAt,
			StopAfter:   flags.stopAfter,
			SeedOutputs: seedOutputs,
		}
	}

	// --checkpoint: attach atomic-write callback.
	if flags.checkpointPath != "" {
		if runOpts == nil {
			runOpts = &runtime.RunFlowOptions{}
		}
		cpPath := flags.checkpointPath
		runOpts.OnCheckpoint = func(snap runtime.Snapshot) error {
			data, err := json.Marshal(snap)
			if err != nil {
				return err
			}
			tmp := cpPath + ".tmp"
			if err := os.WriteFile(tmp, data, 0644); err != nil {
				return err
			}
			return os.Rename(tmp, cpPath)
		}
	}

	// --resume: set StopAfter if --to was also given.
	if flags.resumePath != "" && flags.stopAfter != "" {
		if runOpts == nil {
			runOpts = &runtime.RunFlowOptions{}
		}
		runOpts.StopAfter = flags.stopAfter
	}

	var (
		out    map[string]any
		runErr error
	)
	if flags.resumePath != "" {
		data, err := os.ReadFile(flags.resumePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading snapshot file: %v\n", err)
			os.Exit(1)
		}
		var snap runtime.Snapshot
		if err := json.Unmarshal(data, &snap); err != nil {
			fmt.Fprintf(os.Stderr, "error parsing snapshot file %q: %v\n", flags.resumePath, err)
			os.Exit(1)
		}
		if flags.dataDir != "" && flags.runID != "" && flags.runID != snap.RunID {
			fmt.Fprintf(os.Stderr, "error: --run-id %q does not match snapshot run_id %q\n", flags.runID, snap.RunID)
			os.Exit(1)
		}
		out, runErr = runtime.RunFlowResume(ctx, b, snap, reg, provider, runOpts)
	} else {
		out, runErr = runtime.RunFlow(ctx, b, flags.inputs, reg, provider, runOpts)
	}

	// Persist run result when SDK mode is active.
	if flags.dataDir != "" {
		if runErr != nil {
			writeMeta(runDir, runMeta{
				RunID:      flags.runID,
				Bundle:     bundlePath,
				StartedAt:  startedAt,
				FinishedAt: time.Now().UnixMilli(),
				Status:     "error",
				Error:      runErr.Error(),
			})
		} else {
			data, _ := json.MarshalIndent(out, "", "  ")
			os.WriteFile(filepath.Join(runDir, "output.json"), data, 0644)
			writeMeta(runDir, runMeta{
				RunID:      flags.runID,
				Bundle:     bundlePath,
				StartedAt:  startedAt,
				FinishedAt: time.Now().UnixMilli(),
				Status:     "done",
			})
		}
	}

	if runErr != nil {
		fmt.Fprintf(os.Stderr, "error running flow: %v\n", runErr)
		os.Exit(1)
	}

	// In CLI mode, print the final output to stdout. SDK mode consumers read output.json instead.
	if flags.dataDir == "" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(out); err != nil {
			fmt.Fprintf(os.Stderr, "error encoding output: %v\n", err)
			os.Exit(1)
		}
	}
}

// runFlags holds all parsed flags for the run subcommand.
type runFlags struct {
	inputs         map[string]any
	tracePath      string
	tools          map[string]runtime.Tool
	dataDir        string
	runID          string
	startAt        string
	stopAfter      string
	seedPath       string
	checkpointPath string
	resumePath     string
}

// parseRunFlags parses --input, --trace, --tool, --data-dir, and --run-id flags.
//
//	--input key=value                    flow input (repeatable)
//	--trace <file>                       write JSON trace events to file
//	--tool name@ver=https://host/path    HTTP tool (POST args as JSON to URL)
//	--tool name@ver='<json>'             stub tool returning fixed JSON object
//	--tool name@ver                      stub tool returning {}
//	--data-dir <path>                    SDK mode: stream events to stdout + persist run
//	--run-id <id>                        run identifier (required with --data-dir)
func parseRunFlags(args []string) (runFlags, error) {
	f := runFlags{
		inputs: make(map[string]any),
		tools:  make(map[string]runtime.Tool),
	}
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--input":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--input requires a key=value argument")
			}
			kv := args[i]
			idx := strings.IndexByte(kv, '=')
			if idx <= 0 {
				return f, fmt.Errorf("--input %q: expected key=value format", kv)
			}
			key, val := kv[:idx], kv[idx+1:]
			if strings.HasPrefix(val, "@") {
				fv, err := loadFileInput(val[1:])
				if err != nil {
					return f, fmt.Errorf("--input %s: %w", key, err)
				}
				f.inputs[key] = fv
			} else {
				f.inputs[key] = val
			}
		case "--trace":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--trace requires a file path argument")
			}
			f.tracePath = args[i]
		case "--tool":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--tool requires a name@version argument")
			}
			ref, tool, parseErr := parseToolArg(args[i])
			if parseErr != nil {
				return f, fmt.Errorf("--tool: %w", parseErr)
			}
			f.tools[ref] = tool
		case "--data-dir":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--data-dir requires a path argument")
			}
			f.dataDir = args[i]
		case "--run-id":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--run-id requires an id argument")
			}
			f.runID = args[i]
		case "--from":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--from requires a node name argument")
			}
			f.startAt = args[i]
		case "--to":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--to requires a node name argument")
			}
			f.stopAfter = args[i]
		case "--seed":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--seed requires a file path argument")
			}
			f.seedPath = args[i]
		case "--checkpoint":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--checkpoint requires a file path argument")
			}
			f.checkpointPath = args[i]
		case "--resume":
			i++
			if i >= len(args) {
				return f, fmt.Errorf("--resume requires a file path argument")
			}
			f.resumePath = args[i]
		default:
			return f, fmt.Errorf("unexpected argument %q", args[i])
		}
	}
	return f, nil
}

// runMeta is the structure persisted as meta.json inside a run directory.
type runMeta struct {
	RunID      string `json:"run_id"`
	Bundle     string `json:"bundle"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	Status     string `json:"status"`
	Error      string `json:"error,omitempty"`
}

// writeMeta serialises m as indented JSON to <runDir>/meta.json.
func writeMeta(runDir string, m runMeta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runDir, "meta.json"), data, 0644)
}

// parseToolArg parses a --tool argument and returns the ref and Tool implementation.
//
//	name@version                          stub returning {}
//	name@version='{"key":"val"}'         stub returning fixed JSON
//	name@version=https://host/path       HTTPTool (POST to URL)
//	name@version=http://host/path        HTTPTool (POST to URL)
func parseToolArg(arg string) (ref string, tool runtime.Tool, err error) {
	idx := strings.IndexByte(arg, '=')
	if idx < 0 {
		return arg, runtime.ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
			return map[string]any{}, nil
		}), nil
	}

	ref = arg[:idx]
	value := arg[idx+1:]

	if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
		return ref, runtime.NewHTTPTool(value), nil
	}

	var response map[string]any
	if err := json.Unmarshal([]byte(value), &response); err != nil {
		return "", nil, fmt.Errorf("tool %q: value is neither an HTTP URL nor valid JSON: %w", ref, err)
	}
	captured := response
	return ref, runtime.ToolFunc(func(_ context.Context, _ map[string]any) (map[string]any, error) {
		return captured, nil
	}), nil
}

// loadFileInput reads a file from path and returns a FileValue with detected MIME type.
func loadFileInput(path string) (runtime.FileValue, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return runtime.FileValue{}, fmt.Errorf("reading file %q: %w", path, err)
	}

	mediaType := detectMIMEType(path, data)
	return runtime.FileValue{
		Name:      filepath.Base(path),
		Data:      data,
		MediaType: mediaType,
	}, nil
}

// detectMIMEType detects the MIME type of file data, using the file extension
// as a fallback for types that net/http.DetectContentType does not recognise.
func detectMIMEType(path string, data []byte) string {
	// Extension-based detection for formats http.DetectContentType misses.
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".md", ".markdown":
		return "text/markdown"
	case ".csv":
		return "text/csv"
	case ".html", ".htm":
		return "text/html"
	case ".xml":
		return "text/xml"
	case ".json":
		return "application/json"
	}

	// Fallback to content sniffing (reliable for images).
	sniff := data
	if len(sniff) > 512 {
		sniff = sniff[:512]
	}
	return http.DetectContentType(sniff)
}

// knownProviderEnvVars maps provider prefix (from "provider/model") to its env var.
// Only providers listed here are checked; unknown prefixes are silently skipped.
var knownProviderEnvVars = map[string]string{
	"anthropic": "ANTHROPIC_API_KEY",
	"openai":    "OPENAI_API_KEY",
	"gemini":    "GEMINI_API_KEY",
}

type missingKeyInfo struct {
	envVar string
	models []string
	nodes  []string
}

// collectMissingKeys traverses only the nodes reachable from the bundle's entry flow
// (recursing into subflows) and returns one missingKeyInfo per env var that is unset
// but required by a node's config.model. Unreachable node versions are not checked.
func collectMissingKeys(b *bundle.Bundle) []missingKeyInfo {
	type detail struct {
		models map[string]bool
		nodes  map[string]bool
	}
	byEnv := make(map[string]*detail)
	visited := make(map[string]bool)

	var visitFlow func(name, version string)
	visitFlow = func(name, version string) {
		key := name + "@" + version
		if visited[key] {
			return
		}
		visited[key] = true

		flow, ok := b.Flows[name][version]
		if !ok {
			return
		}

		for localName, ref := range flow.Nodes {
			nodeName, nodeVersion, ok := bundle.ParseRef(ref)
			if !ok {
				continue
			}
			node, ok := b.Nodes[nodeName][nodeVersion]
			if !ok {
				continue
			}

			if modelRaw, ok := node.Config["model"]; ok {
				var model string
				if err := json.Unmarshal(modelRaw, &model); err == nil {
					if idx := strings.IndexByte(model, '/'); idx != -1 {
						provider := model[:idx]
						if envVar, known := knownProviderEnvVars[provider]; known && os.Getenv(envVar) == "" {
							if byEnv[envVar] == nil {
								byEnv[envVar] = &detail{
									models: make(map[string]bool),
									nodes:  make(map[string]bool),
								}
							}
							byEnv[envVar].models[model] = true
							byEnv[envVar].nodes[localName] = true
						}
					}
				}
			}

			if node.Type == "subflow" {
				if flowRaw, ok := node.Config["flow"]; ok {
					var flowRef string
					if err := json.Unmarshal(flowRaw, &flowRef); err == nil {
						if fn, fv, ok := bundle.ParseRef(flowRef); ok {
							visitFlow(fn, fv)
						}
					}
				}
			}
		}
	}

	entryName, entryVersion, ok := bundle.ParseRef(b.Manifest.Entry)
	if !ok {
		return nil
	}
	visitFlow(entryName, entryVersion)

	result := make([]missingKeyInfo, 0, len(byEnv))
	for envVar, d := range byEnv {
		info := missingKeyInfo{envVar: envVar}
		for m := range d.models {
			info.models = append(info.models, m)
		}
		for n := range d.nodes {
			info.nodes = append(info.nodes, n)
		}
		sort.Strings(info.models)
		sort.Strings(info.nodes)
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].envVar < result[j].envVar })
	return result
}

// buildProviderRegistry creates a ProviderRegistry populated from environment variables.
// ANTHROPIC_API_KEY, OPENAI_API_KEY, and GEMINI_API_KEY are checked.
func buildProviderRegistry() *runtime.ProviderRegistry {
	reg := runtime.NewProviderRegistry("anthropic")

	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		client := anthropic.NewClient()
		reg.Register("anthropic", runtime.NewAnthropicProvider(&client))
	}

	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		client := openai.NewClient()
		_ = key
		reg.Register("openai", runtime.NewOpenAIProvider(&client))
	}

	if key := os.Getenv("GEMINI_API_KEY"); key != "" {
		client, err := genai.NewClient(context.Background(), &genai.ClientConfig{
			APIKey: key,
		})
		if err == nil {
			reg.Register("gemini", runtime.NewGeminiProvider(client))
		}
	}

	return reg
}
