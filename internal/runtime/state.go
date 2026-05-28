package runtime

import "sync"

// ExecutionContext carries flow inputs, per-node outputs, and the name of the
// currently-executing node through a single flow run.
//
// It is safe for concurrent use. Linear flows write serially, but the lock
// keeps the contract honest for upcoming parallel/map primitives.
type ExecutionContext struct {
	mu          sync.RWMutex
	inputs      map[string]any
	nodeOutputs map[string]any
	currentNode string
	iterVars    map[string]any // iteration variables set by map nodes
}

// NewExecutionContext seeds a context with the flow's inputs. The inputs map
// is copied so the caller's map cannot mutate run state afterwards.
func NewExecutionContext(inputs map[string]any) *ExecutionContext {
	in := make(map[string]any, len(inputs))
	for k, v := range inputs {
		in[k] = v
	}
	return &ExecutionContext{
		inputs:      in,
		nodeOutputs: make(map[string]any),
	}
}

// Inputs returns the flow inputs map. Callers must not mutate it.
func (c *ExecutionContext) Inputs() map[string]any {
	return c.inputs
}

// SetNodeOutput records the output of a node by its flow-local name.
func (c *ExecutionContext) SetNodeOutput(localName string, output any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nodeOutputs[localName] = output
}

// NodeOutput returns the output recorded for a node, if any.
func (c *ExecutionContext) NodeOutput(localName string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out, ok := c.nodeOutputs[localName]
	return out, ok
}

// SetCurrentNode records which node is currently executing.
func (c *ExecutionContext) SetCurrentNode(localName string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.currentNode = localName
}

// CurrentNode returns the flow-local name of the node currently executing.
func (c *ExecutionContext) CurrentNode() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.currentNode
}

// SetIterVar sets an iteration variable by name. Called by map nodes before
// executing each do-node iteration. Not safe for concurrent map execution
// without per-iteration ExecutionContext clones.
func (c *ExecutionContext) SetIterVar(name string, value any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.iterVars == nil {
		c.iterVars = make(map[string]any)
	}
	c.iterVars[name] = value
}

// ClearIterVar removes an iteration variable set by SetIterVar.
func (c *ExecutionContext) ClearIterVar(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.iterVars, name)
}

// IterVar returns the iteration variable registered under name, if any.
func (c *ExecutionContext) IterVar(name string) (any, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.iterVars == nil {
		return nil, false
	}
	v, ok := c.iterVars[name]
	return v, ok
}

// AllNodeOutputs returns a copy of the recorded node outputs map.
func (c *ExecutionContext) AllNodeOutputs() map[string]any {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]any, len(c.nodeOutputs))
	for k, v := range c.nodeOutputs {
		out[k] = v
	}
	return out
}

// Clone returns a new ExecutionContext with a snapshot of the current node
// outputs and a shared reference to the immutable inputs map. The clone has
// its own iterVars and currentNode, so concurrent map iterations can call
// SetIterVar / SetNodeOutput / SetCurrentNode without racing.
func (c *ExecutionContext) Clone() *ExecutionContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	nodeOutputsCopy := make(map[string]any, len(c.nodeOutputs))
	for k, v := range c.nodeOutputs {
		nodeOutputsCopy[k] = v
	}
	return &ExecutionContext{
		inputs:      c.inputs, // immutable after NewExecutionContext; safe to share
		nodeOutputs: nodeOutputsCopy,
	}
}
