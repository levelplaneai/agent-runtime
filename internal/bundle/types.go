package bundle

import "encoding/json"

type Manifest struct {
	BundleVersion  string   `json:"bundle_version"`
	RuntimeVersion string   `json:"runtime_version"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Entry          string   `json:"entry"`
	ToolsRequired  []string `json:"tools_required"`
}

type FlowInputField struct {
	Type string `json:"type"`
}

type FlowOutputBinding struct {
	From string `json:"from"`
}

type Edge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type Flow struct {
	Description string                       `json:"description"`
	Inputs      map[string]FlowInputField    `json:"inputs"`
	Outputs     map[string]FlowOutputBinding `json:"outputs"`
	Entry       string                       `json:"entry"`
	Nodes       map[string]string            `json:"nodes"`
	Edges       []Edge                       `json:"edges"`
}

type InputBinding struct {
	From string `json:"from"`
	Type string `json:"type,omitempty"` // "file_path" → runtime reads file into FileValue
}

type Node struct {
	Type         string                     `json:"type"`
	Description  string                     `json:"description"`
	Inputs       map[string]InputBinding    `json:"inputs"`
	OutputSchema map[string]json.RawMessage `json:"output_schema"`
	Config       map[string]json.RawMessage `json:"config"`
	OnError      string                     `json:"on_error"`
}

type ToolSignature struct {
	Description    string             `json:"description"`
	InputSchema    json.RawMessage    `json:"input_schema"`
	OutputSchema   json.RawMessage    `json:"output_schema"`
}

// Bundle is the fully loaded in-memory representation of a .agent directory.
type Bundle struct {
	Path     string
	Manifest Manifest
	Flows    map[string]map[string]Flow          // name → version → Flow
	Nodes    map[string]map[string]Node          // name → version → Node
	Tools    map[string]map[string]ToolSignature // name → version → ToolSignature
}
