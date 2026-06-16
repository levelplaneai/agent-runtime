package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/levelplaneai/agent-runtime/internal/bundle"
)

// Tool is implemented by host applications to provide callable tools to the runtime.
type Tool interface {
	Call(ctx context.Context, inputs map[string]any) (map[string]any, error)
}

// ToolFunc is a convenience adapter so plain functions satisfy Tool.
type ToolFunc func(ctx context.Context, inputs map[string]any) (map[string]any, error)

func (f ToolFunc) Call(ctx context.Context, inputs map[string]any) (map[string]any, error) {
	return f(ctx, inputs)
}

type toolEntry struct {
	sig  bundle.ToolSignature
	tool Tool
}

// Registry holds tools registered by the host application, keyed by "name@version".
// It is safe for concurrent use: writes happen at startup, reads happen during execution.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]toolEntry
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tools: make(map[string]toolEntry)}
}

// Register adds a tool with its declared signature. ref must be "name@version".
// Registering the same ref twice replaces the previous entry.
func (r *Registry) Register(ref string, sig bundle.ToolSignature, tool Tool) error {
	if !validRef(ref) {
		return fmt.Errorf("tool ref %q must use name@version format", ref)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tools[ref] = toolEntry{sig: sig, tool: tool}
	return nil
}

// Lookup retrieves a tool and its signature by "name@version" ref.
func (r *Registry) Lookup(ref string) (Tool, bundle.ToolSignature, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e, ok := r.tools[ref]
	if !ok {
		return nil, bundle.ToolSignature{}, false
	}
	return e.tool, e.sig, true
}

// MissingTools returns refs from manifest.tools_required that have no registered tool.
// Built-in provider tools ("provider:name" format) are handled natively by the LLM
// provider and are never registered in the registry, so they are skipped.
// Call this after registering all tools and before running a bundle.
func (r *Registry) MissingTools(b *bundle.Bundle) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var missing []string
	for _, ref := range b.Manifest.ToolsRequired {
		if isBuiltinToolRef(ref) {
			continue
		}
		if _, ok := r.tools[ref]; !ok {
			missing = append(missing, ref)
		}
	}
	return missing
}

// isBuiltinToolRef reports whether ref is a provider-managed built-in tool
// in "provider:name" format (e.g. "gemini:code_execution", "anthropic:web_search").
func isBuiltinToolRef(ref string) bool {
	parts := strings.SplitN(ref, ":", 2)
	return len(parts) == 2 && parts[0] != "" && parts[1] != "" && !strings.Contains(parts[1], "@")
}

// validRef reports whether s is a non-empty "name@version" string.
func validRef(s string) bool {
	parts := strings.SplitN(s, "@", 2)
	return len(parts) == 2 && parts[0] != "" && parts[1] != ""
}
