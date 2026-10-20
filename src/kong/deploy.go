package kong

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kong/kong-loadgen/src/config"
)

// Configure pushes the generated configuration to Konnect. With kongctl it
// manages the control plane declaratively and delegates gateway entities to
// decK through the control plane's _deck block.
func Configure(ctx context.Context, cfg config.Config, configPath string, dryRun, generateOnly bool, output io.Writer) error {
	directory, err := writeGeneratedFiles(cfg, configPath)
	if err != nil {
		return err
	}
	fmt.Fprintf(output, "Generated Kong files in %s\n", directory)
	if generateOnly {
		return nil
	}
	if err := requireTools(cfg.Konnect.Tool); err != nil {
		return err
	}
	if cfg.Konnect.Tool == "kongctl" {
		err = configureWithKongctl(ctx, cfg, directory, dryRun, output)
	} else {
		err = configureWithDeck(ctx, cfg, directory, dryRun, output)
	}
	if err != nil || dryRun {
		return err
	}
	parts, err := Fingerprint(cfg)
	if err != nil {
		return err
	}
	return recordApplied(directory, parts)
}

// TeardownOptions controls Teardown.
type TeardownOptions struct {
	AssumeYes bool     // delete without the confirmation prompt
	APINames  []string // Konnect APIs to delete too; empty unless --with-apis
}

// Teardown removes only what this tool created: the Konnect APIs it named, the
// Gateway entities tagged kong-loadgen, and the control plane when this tool
// created it. Previews are shown first, and one confirmation covers them all.
func Teardown(ctx context.Context, cfg config.Config, configPath string, opts TeardownOptions, output io.Writer, input io.Reader) error {
	directory, err := writeGeneratedFiles(cfg, configPath)
	if err != nil {
		return err
	}
	if err := requireTools(cfg.Konnect.Tool); err != nil {
		return err
	}
	ownsControlPlane := cfg.Konnect.Tool == "kongctl" && cfg.Konnect.CreateControlPlane
	controlPlane, found, err := findControlPlane(ctx, cfg)
	if err != nil {
		return err
	}
	if found && ownsControlPlane && controlPlane.Labels["managed-by"] != ManagedTag {
		return fmt.Errorf("control plane %q was not created by kong-loadgen (missing label managed-by=%s); refusing to delete it", cfg.Konnect.ControlPlane, ManagedTag)
	}
	doomedAPIs, err := apisToDelete(ctx, cfg, opts.APINames)
	if err != nil {
		return err
	}
	if !found && len(doomedAPIs) == 0 {
		fmt.Fprintf(output, "Control plane %q does not exist; nothing to tear down.\n", cfg.Konnect.ControlPlane)
		return clearApplied(directory)
	}

	apiDeletePath := filepath.Join(directory, apisDeleteFile)
	if len(doomedAPIs) > 0 {
		manifest, err := APIsDeleteManifest(doomedAPIs)
		if err != nil {
			return err
		}
		if err := os.WriteFile(apiDeletePath, manifest, 0o600); err != nil {
			return fmt.Errorf("write generated/%s: %w", apisDeleteFile, err)
		}
		fmt.Fprintf(output, "Planned removal of APIs: %s\n", strings.Join(doomedAPIs, ", "))
		if err := runKongctl(ctx, cfg, output, "diff", "--mode", "delete", "--filename", apiDeletePath); err != nil {
			return err
		}
	}

	state := filepath.Join(directory, "teardown.yaml")
	if found {
		fmt.Fprintln(output, "Planned removal of Gateway entities tagged kong-loadgen:")
		if err := runDeck(ctx, cfg, output, "gateway", "diff", "--select-tag", ManagedTag, state); err != nil {
			return err
		}
		if ownsControlPlane {
			fmt.Fprintf(output, "Control plane %q will also be deleted.\n", cfg.Konnect.ControlPlane)
		}
	}
	if !opts.AssumeYes {
		confirmed, err := confirm(input, output)
		if err != nil {
			return err
		}
		if !confirmed {
			fmt.Fprintln(output, "Teardown cancelled.")
			return nil
		}
	}

	if len(doomedAPIs) > 0 {
		// APIs go first: their implementations refer to the gateway services.
		if err := runKongctl(ctx, cfg, output, "delete", "--filename", apiDeletePath, "--auto-approve"); err != nil {
			return err
		}
	}
	if found {
		if err := runDeck(ctx, cfg, output, "gateway", "sync", "--select-tag", ManagedTag, "--yes", state); err != nil {
			return err
		}
		if ownsControlPlane {
			manifest := filepath.Join(directory, "control-plane.yaml")
			if err := runKongctl(ctx, cfg, output, "delete", "--filename", manifest, "--auto-approve"); err != nil {
				return err
			}
			fmt.Fprintf(output, "Deleted control plane %q.\n", cfg.Konnect.ControlPlane)
		}
	}
	return clearApplied(directory)
}

func configureWithKongctl(ctx context.Context, cfg config.Config, directory string, dryRun bool, output io.Writer) error {
	manifest := filepath.Join(directory, "kongctl.yaml")
	fmt.Fprintln(output, "Planned Konnect control plane changes:")
	if err := runKongctl(ctx, cfg, output, "diff", "--mode", "sync", "--filename", manifest); err != nil {
		return err
	}
	// kongctl diff lists the decK step but does not run it, so preview gateway
	// entities directly. This needs the control plane to exist already.
	_, exists, err := findControlPlane(ctx, cfg)
	if err != nil {
		return err
	}
	state := filepath.Join(directory, "kong.yaml")
	if exists {
		fmt.Fprintln(output, "Planned Gateway changes:")
		if err := runDeck(ctx, cfg, output, "gateway", "diff", "--select-tag", ManagedTag, state); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(output, "Gateway changes will be listed after the control plane is created.")
	}
	if dryRun {
		return nil
	}
	return runKongctl(ctx, cfg, output, "sync", "--filename", manifest, "--auto-approve")
}

func configureWithDeck(ctx context.Context, cfg config.Config, directory string, dryRun bool, output io.Writer) error {
	state := filepath.Join(directory, "kong.yaml")
	fmt.Fprintln(output, "Planned Gateway changes:")
	if err := runDeck(ctx, cfg, output, "gateway", "diff", "--select-tag", ManagedTag, state); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	return runDeck(ctx, cfg, output, "gateway", "sync", "--select-tag", ManagedTag, "--yes", state)
}

func writeGeneratedFiles(cfg config.Config, configPath string) (string, error) {
	generated, err := Generate(cfg)
	if err != nil {
		return "", err
	}
	directory := generatedDirectory(configPath)
	if err := writeGenerated(directory, generated); err != nil {
		return "", err
	}
	return directory, nil
}

func requireTools(tool string) error {
	if tool == "kongctl" {
		if _, err := exec.LookPath("kongctl"); err != nil {
			return missingToolError("kongctl")
		}
		// kongctl runs decK for the gateway entities, so both must be present.
		if _, err := exec.LookPath("deck"); err != nil {
			return missingToolError("deck")
		}
		return nil
	}
	if _, err := exec.LookPath("deck"); err != nil {
		return missingToolError("deck")
	}
	return nil
}

// liveControlPlane is the subset of `kongctl get gateway control-planes` output we use.
type liveControlPlane struct {
	ID     string            `json:"id"`
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
}

func findControlPlane(ctx context.Context, cfg config.Config) (liveControlPlane, bool, error) {
	command := exec.CommandContext(ctx, "kongctl", "get", "gateway", "control-planes", "--region", cfg.Konnect.Region, "-o", "json")
	command.Env = withEnvironment(os.Environ(), "KONGCTL_DEFAULT_KONNECT_PAT", cfg.Konnect.Token)
	var stderr strings.Builder
	command.Stderr = &stderr
	result, err := command.Output()
	if err != nil {
		return liveControlPlane{}, false, fmt.Errorf("list control planes: %s", redact(stderr.String()+err.Error(), cfg.Konnect.Token))
	}
	var planes []liveControlPlane
	if err := json.Unmarshal(result, &planes); err != nil {
		return liveControlPlane{}, false, fmt.Errorf("parse control planes: %w", err)
	}
	var matches []liveControlPlane
	for _, plane := range planes {
		if plane.Name == cfg.Konnect.ControlPlane {
			matches = append(matches, plane)
		}
	}
	switch len(matches) {
	case 0:
		return liveControlPlane{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		return liveControlPlane{}, false, fmt.Errorf("found %d control planes named %q; rename one before continuing", len(matches), cfg.Konnect.ControlPlane)
	}
}

func confirm(input io.Reader, output io.Writer) (bool, error) {
	fmt.Fprint(output, "Continue? Type yes to delete these resources: ")
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, fmt.Errorf("read confirmation: %w", err)
	}
	return strings.TrimSpace(line) == "yes", nil
}

// runDeck runs decK with the Konnect connection flags. The token goes through a
// 0600 temporary file rather than the command line, and is removed afterwards.
func runDeck(ctx context.Context, cfg config.Config, output io.Writer, args ...string) error {
	tokenFile, err := os.CreateTemp("", "kong-loadgen-token-*")
	if err != nil {
		return fmt.Errorf("create temporary token file: %w", err)
	}
	defer os.Remove(tokenFile.Name())
	if _, err := tokenFile.WriteString(cfg.Konnect.Token); err != nil {
		tokenFile.Close()
		return fmt.Errorf("write temporary token file: %w", err)
	}
	if err := tokenFile.Close(); err != nil {
		return fmt.Errorf("close temporary token file: %w", err)
	}
	connection := []string{
		"--konnect-addr", regionBaseURL(cfg.Konnect.Region),
		"--konnect-control-plane-name", cfg.Konnect.ControlPlane,
		"--konnect-token-file", tokenFile.Name(),
	}
	return runTool(ctx, "deck", append(connection, args...), nil, cfg.Konnect.Token, output)
}

func runKongctl(ctx context.Context, cfg config.Config, output io.Writer, args ...string) error {
	args = append(args, "--region", cfg.Konnect.Region)
	env := []string{"KONGCTL_DEFAULT_KONNECT_PAT=" + cfg.Konnect.Token}
	return runTool(ctx, "kongctl", args, env, cfg.Konnect.Token, output)
}

func regionBaseURL(region string) string {
	return "https://" + region + ".api.konghq.com"
}

func writeGenerated(directory string, generated Generated) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create generated directory: %w", err)
	}
	files := map[string][]byte{
		"kong.yaml":          generated.DeckState,
		"teardown.yaml":      generated.DeckTeardown,
		"kongctl.yaml":       generated.KongctlConfig,
		"control-plane.yaml": generated.ControlPlane,
	}
	for name, contents := range files {
		if err := os.WriteFile(filepath.Join(directory, name), contents, 0o600); err != nil {
			return fmt.Errorf("write generated/%s: %w", name, err)
		}
	}
	return nil
}

// runTool runs an external tool, streams its combined output, and redacts the
// Konnect token from everything it prints.
func runTool(ctx context.Context, name string, args, extraEnv []string, secret string, output io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = append(os.Environ(), extraEnv...)
	result, err := command.CombinedOutput()
	if text := redact(string(result), secret); text != "" {
		fmt.Fprint(output, text)
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w", name, errors.New(redact(err.Error(), secret)))
	}
	return nil
}

func redact(text, secret string) string {
	if secret == "" {
		return text
	}
	return strings.ReplaceAll(text, secret, "[REDACTED]")
}

func withEnvironment(environment []string, key, value string) []string {
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		if !strings.HasPrefix(entry, key+"=") {
			result = append(result, entry)
		}
	}
	return append(result, key+"="+value)
}

func missingToolError(name string) error {
	install := "install it and ensure it is on PATH"
	switch name {
	case "kongctl":
		install = "install kongctl: https://developer.konghq.com/kongctl/"
	case "deck":
		install = "install decK: https://developer.konghq.com/deck/"
	}
	return fmt.Errorf("required Kong tool %q was not found on PATH; %s", name, install)
}
