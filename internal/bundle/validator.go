package bundle

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Validate runs all validation rules against a loaded bundle.
// Returns a list of all errors found (not just the first).
func Validate(b *Bundle) []error {
	var errs []error
	errs = append(errs, validateManifest(b)...)
	for flowName, versions := range b.Flows {
		for version, flow := range versions {
			errs = append(errs, validateFlow(b, flowName, version, flow)...)
		}
	}
	return errs
}

// ParseRef splits "name@version" into (name, version, ok).
func ParseRef(ref string) (string, string, bool) {
	parts := strings.SplitN(ref, "@", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// --- Rule 7: manifest.entry references a valid flow version ---
// --- Rule 2 (manifest): tools_required use name@version ---
func validateManifest(b *Bundle) []error {
	var errs []error

	name, version, ok := ParseRef(b.Manifest.Entry)
	if !ok {
		errs = append(errs, fmt.Errorf("manifest.entry %q: must use name@version format", b.Manifest.Entry))
	} else if _, exists := b.Flows[name][version]; !exists {
		errs = append(errs, fmt.Errorf("manifest.entry %q: flow version not found in bundle", b.Manifest.Entry))
	}

	for _, toolRef := range b.Manifest.ToolsRequired {
		if _, _, ok := ParseRef(toolRef); !ok {
			errs = append(errs, fmt.Errorf("manifest.tools_required %q: must use name@version format", toolRef))
		}
	}

	return errs
}

func validateFlow(b *Bundle, flowName, version string, flow Flow) []error {
	var errs []error
	loc := fmt.Sprintf("flows/%s/%s", flowName, version)

	// Rule 1 + 2: all node references in flow.nodes use name@version and exist
	for localName, ref := range flow.Nodes {
		nodeName, nodeVersion, ok := ParseRef(ref)
		if !ok {
			errs = append(errs, fmt.Errorf("%s: node %q ref %q must use name@version format", loc, localName, ref))
			continue
		}
		if _, exists := b.Nodes[nodeName][nodeVersion]; !exists {
			errs = append(errs, fmt.Errorf("%s: node %q ref %q not found in bundle", loc, localName, ref))
		}
	}

	// Rule 6: entry node exists in flow's nodes map
	if flow.Entry != "" {
		if _, exists := flow.Nodes[flow.Entry]; !exists {
			errs = append(errs, fmt.Errorf("%s: entry node %q not in nodes map", loc, flow.Entry))
		}
	} else {
		errs = append(errs, fmt.Errorf("%s: entry is required", loc))
	}

	// Rule 3: edges reference nodes in the flow's nodes map
	for _, edge := range flow.Edges {
		if _, exists := flow.Nodes[edge.From]; !exists {
			errs = append(errs, fmt.Errorf("%s: edge from %q: node not in nodes map", loc, edge.From))
		}
		if _, exists := flow.Nodes[edge.To]; !exists {
			errs = append(errs, fmt.Errorf("%s: edge to %q: node not in nodes map", loc, edge.To))
		}
	}

	// Rule 5: from paths in flow outputs reference nodes that exist
	for outputName, binding := range flow.Outputs {
		if err := validateFromPath(binding.From, flow.Nodes, loc+"/outputs/"+outputName); err != nil {
			errs = append(errs, err)
		}
	}

	// Rule 8: tools referenced in nodes are in manifest.tools_required
	requiredTools := make(map[string]bool, len(b.Manifest.ToolsRequired))
	for _, t := range b.Manifest.ToolsRequired {
		requiredTools[t] = true
	}

	for localName, ref := range flow.Nodes {
		nodeName, nodeVersion, ok := ParseRef(ref)
		if !ok {
			continue
		}
		node, exists := b.Nodes[nodeName][nodeVersion]
		if !exists {
			continue
		}
		errs = append(errs, validateNodeTools(node, localName, loc, requiredTools)...)
		errs = append(errs, validateNodeSubflow(b, node, localName, loc)...)
	}

	// Rule 9: no cycles in the static edge graph
	if cycle := findCycle(flow); cycle != "" {
		errs = append(errs, fmt.Errorf("%s: cycle detected: %s", loc, cycle))
	}

	return errs
}

// validateFromPath checks that a "$.nodeName.output..." path references a node
// that exists in the flow's nodes map. Flow inputs ("$.inputs.*") are always valid.
func validateFromPath(from string, nodes map[string]string, loc string) error {
	if from == "" {
		return nil
	}
	if !strings.HasPrefix(from, "$.") {
		return fmt.Errorf("%s: from %q must start with $.", loc, from)
	}
	parts := strings.SplitN(from[2:], ".", 2)
	root := parts[0]
	if root == "inputs" {
		return nil
	}
	if _, exists := nodes[root]; !exists {
		return fmt.Errorf("%s: from %q references unknown node %q", loc, from, root)
	}
	return nil
}

// validateNodeTools checks tool references inside a node against manifest.tools_required.
func validateNodeTools(node Node, localName, flowLoc string, requiredTools map[string]bool) []error {
	var errs []error
	loc := fmt.Sprintf("%s/node:%s", flowLoc, localName)

	switch node.Type {
	case "tool_call":
		if toolRaw, ok := node.Config["tool"]; ok {
			var toolRef string
			if err := unmarshalString(toolRaw, &toolRef); err == nil && !requiredTools[toolRef] {
				errs = append(errs, fmt.Errorf("%s: tool %q not declared in manifest.tools_required", loc, toolRef))
			}
		}

	case "prompt":
		if toolsRaw, ok := node.Config["tools"]; ok {
			var toolRefs []string
			if err := unmarshalStringSlice(toolsRaw, &toolRefs); err == nil {
				for _, ref := range toolRefs {
					if !requiredTools[ref] {
						errs = append(errs, fmt.Errorf("%s: tool %q not declared in manifest.tools_required", loc, ref))
					}
				}
			}
		}
	}

	return errs
}

// findCycle returns a description of a cycle in the edge graph, or "" if none.
func findCycle(flow Flow) string {
	adj := make(map[string][]string)
	for _, edge := range flow.Edges {
		adj[edge.From] = append(adj[edge.From], edge.To)
	}

	visited := make(map[string]int) // 0=unvisited, 1=in-stack, 2=done
	var path []string

	var dfs func(node string) bool
	dfs = func(node string) bool {
		visited[node] = 1
		path = append(path, node)
		for _, next := range adj[node] {
			if visited[next] == 1 {
				path = append(path, next)
				return true
			}
			if visited[next] == 0 && dfs(next) {
				return true
			}
		}
		path = path[:len(path)-1]
		visited[node] = 2
		return false
	}

	for node := range flow.Nodes {
		if visited[node] == 0 {
			path = nil
			if dfs(node) {
				return strings.Join(path, " → ")
			}
		}
	}
	return ""
}

// validateNodeSubflow checks that a subflow node's config.flow references an
// existing flow version in the bundle.
func validateNodeSubflow(b *Bundle, node Node, localName, flowLoc string) []error {
	if node.Type != "subflow" {
		return nil
	}
	loc := fmt.Sprintf("%s/node:%s", flowLoc, localName)
	var errs []error

	flowRaw, ok := node.Config["flow"]
	if !ok {
		errs = append(errs, fmt.Errorf("%s: subflow node missing required config.flow", loc))
		return errs
	}
	var flowRef string
	if err := unmarshalString(flowRaw, &flowRef); err != nil {
		errs = append(errs, fmt.Errorf("%s: config.flow must be a string: %w", loc, err))
		return errs
	}
	flowName, flowVersion, ok := ParseRef(flowRef)
	if !ok {
		errs = append(errs, fmt.Errorf("%s: config.flow %q must use name@version format", loc, flowRef))
		return errs
	}
	if _, exists := b.Flows[flowName][flowVersion]; !exists {
		errs = append(errs, fmt.Errorf("%s: config.flow %q: flow version not found in bundle", loc, flowRef))
	}
	return errs
}

func unmarshalString(raw json.RawMessage, v *string) error {
	return json.Unmarshal(raw, v)
}

func unmarshalStringSlice(raw json.RawMessage, v *[]string) error {
	return json.Unmarshal(raw, v)
}
