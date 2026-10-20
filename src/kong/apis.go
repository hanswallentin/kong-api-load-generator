package kong

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kong/kong-loadgen/src/apis"
	"github.com/kong/kong-loadgen/src/config"
	"gopkg.in/yaml.v3"
)

// APIsNamespace keeps the Konnect APIs out of the namespace that configure uses
// for the control plane. A control-plane sync deletes anything in its own
// namespace that is missing from its manifest, so the APIs must not share it.
const APIsNamespace = "kong-loadgen-apis"

// apisManifestFile is written to generated/. The specs are inline in it: kongctl's
// !file tag produced specs that Konnect rejected as invalid.
const apisManifestFile = "kongctl-apis.yaml"

// apisDeleteFile is the manifest teardown uses to delete the APIs it named.
const apisDeleteFile = "kongctl-apis-delete.yaml"

// APIsManifest builds the kongctl manifest for the APIs. It refers to the control
// plane and to the gateway services by name, which the control-plane sync created,
// so each API is implemented by the service that serves its endpoint.
func APIsManifest(cfg config.Config, items []apis.API) ([]byte, error) {
	controlPlane := cfg.Konnect.ControlPlane
	var services, definitions []*yaml.Node
	for _, item := range items {
		serviceRef := item.Name + "-svc"
		services = append(services, mapNode(
			"ref", strNode(serviceRef),
			"_external", externalByName(item.Name),
		))
		definitions = append(definitions, apiNode(item, controlPlane, serviceRef))
	}
	document := mapNode(
		"control_planes", seqNode(mapNode(
			"ref", strNode(controlPlane),
			"_external", externalByName(controlPlane),
			"gateway_services", seqNode(services...),
		)),
		"apis", seqNode(definitions...),
	)
	return marshalNode(document)
}

// APIsDeleteManifest lists the named APIs with no implementations. kongctl deletes
// the APIs by name and needs no control plane, so teardown still works when the
// control plane is already gone.
func APIsDeleteManifest(names []string) ([]byte, error) {
	var definitions []*yaml.Node
	for _, name := range names {
		definitions = append(definitions, mapNode(
			"ref", strNode(name),
			"name", strNode(name),
			"labels", mapNode("managed-by", strNode(ManagedTag)),
			"kongctl", mapNode("namespace", strNode(APIsNamespace)),
		))
	}
	return marshalNode(mapNode("apis", seqNode(definitions...)))
}

func apiNode(item apis.API, controlPlane, serviceRef string) *yaml.Node {
	return mapNode(
		"ref", strNode(item.Name),
		"name", strNode(item.Name),
		"description", strNode(item.Description),
		"labels", mapNode("managed-by", strNode(ManagedTag)),
		"kongctl", mapNode("namespace", strNode(APIsNamespace)),
		"versions", seqNode(mapNode(
			"ref", strNode(item.Name+"-v1"),
			"version", quotedNode("1.0.0"),
			"spec", mapNode("content", strNode(item.Spec)),
		)),
		"implementations", seqNode(mapNode(
			"ref", strNode(item.Name+"-impl"),
			"service", mapNode(
				"id", taggedNode("!ref", serviceRef+"#id"),
				"control_plane_id", taggedNode("!ref", controlPlane+"#id"),
			),
		)),
	)
}

func externalByName(name string) *yaml.Node {
	return mapNode("selector", mapNode("matchFields", mapNode("name", strNode(name))))
}

// ConfigureAPIs writes the APIs into generated/ and applies them. The control plane
// must exist, because the implementations refer to it and to its services.
func ConfigureAPIs(ctx context.Context, cfg config.Config, configPath string, items []apis.API, dryRun, generateOnly bool, output io.Writer) error {
	manifest, err := APIsManifest(cfg, items)
	if err != nil {
		return err
	}
	directory := generatedDirectory(configPath)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create generated directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(directory, apisManifestFile), manifest, 0o600); err != nil {
		return fmt.Errorf("write generated/%s: %w", apisManifestFile, err)
	}
	fmt.Fprintf(output, "Generated %d API(s) in %s\n", len(items), filepath.Join(directory, apisManifestFile))
	if generateOnly {
		return nil
	}

	if _, exists, err := findControlPlane(ctx, cfg); err != nil {
		return err
	} else if !exists {
		fmt.Fprintln(output, "APIs will be listed after the control plane is created.")
		return nil
	}
	path := filepath.Join(directory, apisManifestFile)
	fmt.Fprintln(output, "Planned API changes:")
	if err := runKongctl(ctx, cfg, output, "diff", "--mode", "sync", "--filename", path); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	return runKongctl(ctx, cfg, output, "sync", "--filename", path, "--auto-approve")
}

// existingAPILabels returns the labels of each named API that exists in Konnect.
func existingAPILabels(ctx context.Context, cfg config.Config, names []string) (map[string]map[string]string, error) {
	command := exec.CommandContext(ctx, "kongctl", "get", "apis", "--region", cfg.Konnect.Region, "-o", "json")
	command.Env = withEnvironment(os.Environ(), "KONGCTL_DEFAULT_KONNECT_PAT", cfg.Konnect.Token)
	var stderr strings.Builder
	command.Stderr = &stderr
	result, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list APIs: %s", redact(stderr.String()+err.Error(), cfg.Konnect.Token))
	}
	var listed []struct {
		Name   string            `json:"name"`
		Labels map[string]string `json:"labels"`
	}
	if err := json.Unmarshal(result, &listed); err != nil {
		return nil, fmt.Errorf("parse APIs: %w", err)
	}
	wanted := map[string]bool{}
	for _, name := range names {
		wanted[name] = true
	}
	found := map[string]map[string]string{}
	for _, api := range listed {
		if wanted[api.Name] {
			found[api.Name] = api.Labels
		}
	}
	return found, nil
}

// apisToDelete returns the named APIs that exist and carry the managed-by label.
// It refuses if a named API exists but was not created by this tool. It does not
// delete anything; teardown does that after the user confirms.
func apisToDelete(ctx context.Context, cfg config.Config, names []string) ([]string, error) {
	found, err := existingAPILabels(ctx, cfg, names)
	if err != nil {
		return nil, err
	}
	var doomed []string
	for _, name := range names {
		labels, exists := found[name]
		if !exists {
			continue
		}
		if labels["managed-by"] != ManagedTag {
			return nil, fmt.Errorf("API %q was not created by kong-loadgen (missing label managed-by=%s); refusing to delete it", name, ManagedTag)
		}
		doomed = append(doomed, name)
	}
	return doomed, nil
}

func mapNode(pairs ...any) *yaml.Node {
	node := &yaml.Node{Kind: yaml.MappingNode}
	for i := 0; i+1 < len(pairs); i += 2 {
		node.Content = append(node.Content, strNode(pairs[i].(string)), pairs[i+1].(*yaml.Node))
	}
	return node
}

func seqNode(items ...*yaml.Node) *yaml.Node {
	return &yaml.Node{Kind: yaml.SequenceNode, Content: items}
}

func strNode(value string) *yaml.Node {
	node := &yaml.Node{Kind: yaml.ScalarNode, Value: value}
	if strings.Contains(value, "\n") {
		node.Style = yaml.LiteralStyle
	}
	return node
}

// quotedNode is a string that must stay a string, such as a version number.
func quotedNode(value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Style: yaml.DoubleQuotedStyle, Value: value}
}

// taggedNode is a scalar with a kongctl tag, such as !ref.
func taggedNode(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

func marshalNode(node *yaml.Node) ([]byte, error) {
	var buffer strings.Builder
	encoder := yaml.NewEncoder(&buffer)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return nil, fmt.Errorf("encode APIs manifest: %w", err)
	}
	if err := encoder.Close(); err != nil {
		return nil, fmt.Errorf("encode APIs manifest: %w", err)
	}
	return []byte(buffer.String()), nil
}
