package bundle

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

func Load(dir string) (*Bundle, error) {
	b := &Bundle{
		Path:  dir,
		Flows: make(map[string]map[string]Flow),
		Nodes: make(map[string]map[string]Node),
		Tools: make(map[string]map[string]ToolSignature),
	}

	if err := loadManifest(dir, b); err != nil {
		return nil, err
	}
	if err := loadFlows(dir, b); err != nil {
		return nil, err
	}
	if err := loadNodes(dir, b); err != nil {
		return nil, err
	}
	if err := loadTools(dir, b); err != nil {
		return nil, err
	}

	return b, nil
}

func loadManifest(dir string, b *Bundle) error {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return fmt.Errorf("manifest.json: %w", err)
	}
	return json.Unmarshal(data, &b.Manifest)
}

func loadFlows(dir string, b *Bundle) error {
	return loadVersionedEntities(filepath.Join(dir, "flows"), func(name, version string, data []byte) error {
		var f Flow
		if err := json.Unmarshal(data, &f); err != nil {
			return fmt.Errorf("flows/%s/%s/flow.json: %w", name, version, err)
		}
		if b.Flows[name] == nil {
			b.Flows[name] = make(map[string]Flow)
		}
		b.Flows[name][version] = f
		return nil
	}, "flow.json")
}

func loadNodes(dir string, b *Bundle) error {
	return loadVersionedEntities(filepath.Join(dir, "nodes"), func(name, version string, data []byte) error {
		var n Node
		if err := json.Unmarshal(data, &n); err != nil {
			return fmt.Errorf("nodes/%s/%s/node.json: %w", name, version, err)
		}
		if b.Nodes[name] == nil {
			b.Nodes[name] = make(map[string]Node)
		}
		b.Nodes[name][version] = n
		return nil
	}, "node.json")
}

func loadTools(dir string, b *Bundle) error {
	return loadVersionedEntities(filepath.Join(dir, "tools"), func(name, version string, data []byte) error {
		var t ToolSignature
		if err := json.Unmarshal(data, &t); err != nil {
			return fmt.Errorf("tools/%s/%s/signature.json: %w", name, version, err)
		}
		if b.Tools[name] == nil {
			b.Tools[name] = make(map[string]ToolSignature)
		}
		b.Tools[name][version] = t
		return nil
	}, "signature.json")
}

// loadVersionedEntities walks name/version/ subdirectories under root and calls
// fn for each found file matching filename.
func loadVersionedEntities(root string, fn func(name, version string, data []byte) error, filename string) error {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}

	for _, nameEntry := range entries {
		if !nameEntry.IsDir() {
			continue
		}
		name := nameEntry.Name()
		versionsDir := filepath.Join(root, name)

		versions, err := os.ReadDir(versionsDir)
		if err != nil {
			return err
		}

		for _, vEntry := range versions {
			if !vEntry.IsDir() {
				continue
			}
			version := vEntry.Name()
			filePath := filepath.Join(versionsDir, version, filename)

			data, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("reading %s: %w", filePath, err)
			}

			if err := fn(name, version, data); err != nil {
				return err
			}
		}
	}

	return nil
}
