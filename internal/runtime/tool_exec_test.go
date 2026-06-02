package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestExecTool_Success(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "exec.py")
	if err := os.WriteFile(script, []byte(`
import sys, json
args = json.load(sys.stdin)
json.dump({"echo": args.get("msg"), "ok": True}, sys.stdout)
`), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewExecTool("python3 exec.py", dir)
	out, err := tool.Call(context.Background(), map[string]any{"msg": "hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["echo"] != "hello" {
		t.Errorf("expected echo=hello, got %v", out["echo"])
	}
	if out["ok"] != true {
		t.Errorf("expected ok=true, got %v", out["ok"])
	}
}

func TestExecTool_NonZeroExit(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "exec.py")
	if err := os.WriteFile(script, []byte(`
import sys
print("something went wrong", file=sys.stderr)
sys.exit(1)
`), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewExecTool("python3 exec.py", dir)
	_, err := tool.Call(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for non-zero exit, got nil")
	}
	if !containsStr(err.Error(), "something went wrong") {
		t.Errorf("expected stderr in error message, got: %v", err)
	}
}

func TestExecTool_InvalidJSONOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "exec.py")
	if err := os.WriteFile(script, []byte(`print("not json")`), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewExecTool("python3 exec.py", dir)
	_, err := tool.Call(context.Background(), map[string]any{})
	if err == nil {
		t.Fatal("expected error for invalid JSON output, got nil")
	}
}

func TestExecTool_ContextCancellation(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "exec.py")
	if err := os.WriteFile(script, []byte(`
import time, sys, json
time.sleep(30)
json.dump({}, sys.stdout)
`), 0o755); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	tool := NewExecTool("python3 exec.py", dir)
	_, err := tool.Call(ctx, map[string]any{})
	if err == nil {
		t.Fatal("expected error for cancelled context, got nil")
	}
}

func TestExecTool_WorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	// Write a helper module and a script that imports it
	if err := os.WriteFile(filepath.Join(dir, "helper.py"), []byte(`
def greet(name): return "hello " + name
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "exec.py"), []byte(`
import sys, json
from helper import greet
args = json.load(sys.stdin)
json.dump({"greeting": greet(args["name"])}, sys.stdout)
`), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewExecTool("python3 exec.py", dir)
	out, err := tool.Call(context.Background(), map[string]any{"name": "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["greeting"] != "hello world" {
		t.Errorf("expected greeting='hello world', got %v", out["greeting"])
	}
}

func TestExecTool_ShellScript(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "exec.sh")
	if err := os.WriteFile(script, []byte(`#!/bin/bash
echo '{"result": "from_shell"}'
`), 0o755); err != nil {
		t.Fatal(err)
	}

	tool := NewExecTool("bash exec.sh", dir)
	out, err := tool.Call(context.Background(), map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out["result"] != "from_shell" {
		t.Errorf("expected result=from_shell, got %v", out["result"])
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
