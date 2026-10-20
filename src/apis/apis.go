// Package apis describes the Konnect APIs that sit in front of each endpoint.
// They give Konnect Analytics an API-level view of the traffic, alongside the
// consumer-level view. Their text lives in apis.yaml, which is read only when
// configure or teardown is run with --with-apis.
package apis

import (
	"fmt"
	"os"
	"strings"

	"github.com/kong/kong-loadgen/src/config"
	"gopkg.in/yaml.v3"
)

// DefaultFile is the APIs file name, beside config.yaml.
const DefaultFile = "apis.yaml"

// Entry is one API. The API is named after its endpoint.
type Entry struct {
	Endpoint    string `yaml:"endpoint"`
	Description string `yaml:"description"`
	Spec        string `yaml:"spec"`
}

// File is the content of apis.yaml.
type File struct {
	APIs []Entry `yaml:"apis"`
}

// API is one Konnect API to create, resolved against the configuration.
type API struct {
	Name        string // the endpoint name
	Description string
	Spec        string // OpenAPI document, as YAML
}

// Read parses apis.yaml. Validate checks it against the configuration.
func Read(path string) (File, *yaml.Node, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return File{}, nil, fmt.Errorf("read %s: %w (create it with: kong-loadgen apis init)", path, err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return File{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	var file File
	if err := document.Decode(&file); err != nil {
		return File{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	return file, documentRoot(&document), nil
}

// Validate reports problems in apis.yaml with line numbers.
func Validate(cfg config.Config, file File, root *yaml.Node) []config.ValidationError {
	var problems []config.ValidationError
	add := func(node *yaml.Node, message string) {
		line := 1
		if node != nil && node.Line > 0 {
			line = node.Line
		}
		problems = append(problems, config.ValidationError{Line: line, Message: message})
	}
	endpoints := map[string]bool{}
	for _, endpoint := range cfg.Endpoints {
		endpoints[endpoint.Name] = true
	}
	entryNodes := sequenceNodes(mappingValue(root)["apis"])
	seen := map[string]bool{}
	for i, entry := range file.APIs {
		var node *yaml.Node
		if i < len(entryNodes) {
			node = entryNodes[i]
		}
		fields := mappingValue(node)
		if !endpoints[entry.Endpoint] {
			add(firstNode(fields["endpoint"], node), fmt.Sprintf("endpoint %q is not defined in config.yaml", entry.Endpoint))
		}
		if seen[entry.Endpoint] {
			add(firstNode(fields["endpoint"], node), fmt.Sprintf("duplicate API for endpoint %q", entry.Endpoint))
		}
		seen[entry.Endpoint] = true
		if strings.TrimSpace(entry.Description) == "" {
			add(firstNode(fields["description"], node), "description is required")
		}
		if strings.TrimSpace(entry.Spec) == "" {
			add(firstNode(fields["spec"], node), "spec is required")
		} else if err := yaml.Unmarshal([]byte(entry.Spec), new(map[string]any)); err != nil {
			add(firstNode(fields["spec"], node), fmt.Sprintf("spec is not valid YAML: %v", err))
		}
	}
	return problems
}

// Resolve returns one API per endpoint, in configuration order. Endpoints with no
// entry get a generated template, and each such endpoint is reported in notes.
func Resolve(cfg config.Config, file File) (apis []API, notes []string) {
	entries := map[string]Entry{}
	for _, entry := range file.APIs {
		entries[entry.Endpoint] = entry
	}
	for _, endpoint := range cfg.Endpoints {
		entry, ok := entries[endpoint.Name]
		if !ok {
			description, spec := Template(endpoint)
			entry = Entry{Endpoint: endpoint.Name, Description: description, Spec: spec}
			notes = append(notes, fmt.Sprintf("no entry for endpoint %q in apis.yaml; using a generated template", endpoint.Name))
		}
		apis = append(apis, API{Name: endpoint.Name, Description: strings.TrimSpace(entry.Description), Spec: entry.Spec})
	}
	return apis, notes
}

// Template returns the generated description and OpenAPI spec for an endpoint.
func Template(endpoint config.Endpoint) (description, spec string) {
	return templateDescription(endpoint), templateSpec(endpoint)
}

func documentRoot(document *yaml.Node) *yaml.Node {
	if document != nil && len(document.Content) > 0 {
		return document.Content[0]
	}
	return document
}

func mappingValue(node *yaml.Node) map[string]*yaml.Node {
	result := map[string]*yaml.Node{}
	if node == nil || node.Kind != yaml.MappingNode {
		return result
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		result[node.Content[i].Value] = node.Content[i+1]
	}
	return result
}

func sequenceNodes(node *yaml.Node) []*yaml.Node {
	if node == nil || node.Kind != yaml.SequenceNode {
		return nil
	}
	return node.Content
}

func firstNode(nodes ...*yaml.Node) *yaml.Node {
	for _, node := range nodes {
		if node != nil {
			return node
		}
	}
	return nil
}
