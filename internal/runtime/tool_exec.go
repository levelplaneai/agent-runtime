package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// ExecTool implements Tool by forking a subprocess.
//
// The runtime writes the tool arguments as a single JSON object to the
// process's stdin. The process must write a JSON object to stdout and exit
// with code 0 on success. Any non-zero exit code is treated as an error; the
// first 400 bytes of stderr are included in the error message.
//
// The working directory is set to Dir (typically the tool's version directory
// inside the bundle). This lets exec.py use relative imports and open sibling
// files without knowing the bundle's absolute path at authoring time.
//
// Example exec.py skeleton:
//
//	import sys, json
//	args = json.load(sys.stdin)
//	# ... do work using args ...
//	json.dump({"key": "value"}, sys.stdout)
type ExecTool struct {
	// Command is the shell command to run, e.g. "python3 exec.py".
	// It is split on whitespace; no shell expansion is performed.
	Command string
	// Dir is the working directory for the subprocess.
	Dir string
}

// NewExecTool returns an ExecTool for the given command and working directory.
func NewExecTool(command, dir string) *ExecTool {
	return &ExecTool{Command: command, Dir: dir}
}

// Call forks the subprocess, writes args as JSON to stdin, and decodes stdout
// as the tool's output map. The context deadline is honoured — if it fires
// before the process exits, the process is killed.
func (t *ExecTool) Call(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	argsJSON, err := json.Marshal(inputs)
	if err != nil {
		return nil, fmt.Errorf("exec tool: marshaling args: %w", err)
	}

	parts := strings.Fields(t.Command)
	if len(parts) == 0 {
		return nil, fmt.Errorf("exec tool: empty command")
	}

	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)
	cmd.Dir = t.Dir
	cmd.Stdin = bytes.NewReader(argsJSON)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	start := time.Now()
	if err := cmd.Run(); err != nil {
		snippet := stderr.String()
		if len(snippet) > 400 {
			snippet = snippet[:400]
		}
		return nil, fmt.Errorf("exec tool %q: %w (after %dms)%s",
			t.Command, err, time.Since(start).Milliseconds(),
			formatStderr(snippet))
	}

	var result map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		preview := stdout.String()
		if len(preview) > 200 {
			preview = preview[:200] + "..."
		}
		return nil, fmt.Errorf("exec tool %q: stdout is not valid JSON: %w\noutput: %s",
			t.Command, err, preview)
	}
	return result, nil
}

func formatStderr(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	return "\nstderr: " + s
}
