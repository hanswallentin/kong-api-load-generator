package kong

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/kong/kong-loadgen/src/config"
)

func Configure(ctx context.Context, cfg config.Config, configPath string, dryRun, generateOnly bool, output io.Writer) error {
	generated, err := Generate(cfg)
	if err != nil {
		return err
	}
	directory := filepath.Join(filepath.Dir(configPath), "generated")
	if err := writeGenerated(directory, generated); err != nil {
		return err
	}
	fmt.Fprintf(output, "Generated Kong files in %s\n", directory)
	if generateOnly {
		return nil
	}
	if cfg.Konnect.Token == "" {
		return fmt.Errorf("Konnect token is required; set KONNECT_TOKEN")
	}
	if _, err := exec.LookPath(cfg.Konnect.Tool); err != nil {
		return missingToolError(cfg.Konnect.Tool)
	}
	if cfg.Konnect.Tool == "kongctl" {
		if _, err := exec.LookPath("deck"); err != nil {
			return fmt.Errorf("kongctl Gateway synchronization requires decK on PATH; install decK from https://developer.konghq.com/deck/")
		}
		return configureWithKongctl(ctx, cfg, directory, dryRun, output)
	}
	return configureWithDeck(ctx, cfg, directory, dryRun, output)
}

func Teardown(ctx context.Context, cfg config.Config, configPath string, output io.Writer) error {
	generated, err := Generate(cfg)
	if err != nil {
		return err
	}
	directory := filepath.Join(filepath.Dir(configPath), "generated")
	if err := writeGenerated(directory, generated); err != nil {
		return err
	}
	if cfg.Konnect.Token == "" {
		return fmt.Errorf("Konnect token is required; set KONNECT_TOKEN")
	}
	if _, err := exec.LookPath("deck"); err != nil {
		return missingToolError("deck")
	}
	deckCommandArgs := deckArgs(cfg, "sync", filepath.Join(directory, "teardown.yaml"))
	deckCommandArgs = append(deckCommandArgs, "--yes")
	fmt.Fprintln(output, "Planned teardown of Kong resources tagged kong-loadgen:")
	if err := runDeckTool(ctx, cfg, deckCommandArgs, output); err != nil {
		return err
	}
	if cfg.Konnect.Tool != "kongctl" || !cfg.Konnect.CreateControlPlane {
		return nil
	}
	if _, err := exec.LookPath("kongctl"); err != nil {
		return missingToolError("kongctl")
	}
	args := []string{"diff", "--mode", "delete", "--filename", filepath.Join(directory, "control-plane.yaml"), "--region", cfg.Konnect.Region}
	fmt.Fprintln(output, "Planned teardown of the kong-loadgen control plane:")
	if err := runTool(ctx, "kongctl", args, "KONGCTL_DEFAULT_KONNECT_PAT", cfg.Konnect.Token, output); err != nil {
		return err
	}
	args = []string{"delete", "--filename", filepath.Join(directory, "control-plane.yaml"), "--region", cfg.Konnect.Region, "--auto-approve"}
	return runTool(ctx, "kongctl", args, "KONGCTL_DEFAULT_KONNECT_PAT", cfg.Konnect.Token, output)
}

func configureWithKongctl(ctx context.Context, cfg config.Config, directory string, dryRun bool, output io.Writer) error {
	manifest := filepath.Join(directory, "kongctl.yaml")
	args := []string{"diff", "--mode", "sync", "--filename", manifest, "--region", cfg.Konnect.Region}
	fmt.Fprintln(output, "Planned Konnect and Gateway changes:")
	if err := runTool(ctx, "kongctl", args, "KONGCTL_DEFAULT_KONNECT_PAT", cfg.Konnect.Token, output); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	args = []string{"sync", "--filename", manifest, "--region", cfg.Konnect.Region, "--auto-approve"}
	return runTool(ctx, "kongctl", args, "KONGCTL_DEFAULT_KONNECT_PAT", cfg.Konnect.Token, output)
}

func configureWithDeck(ctx context.Context, cfg config.Config, directory string, dryRun bool, output io.Writer) error {
	state := filepath.Join(directory, "kong.yaml")
	args := deckArgs(cfg, "diff", state)
	fmt.Fprintln(output, "Planned Gateway changes:")
	if err := runDeckTool(ctx, cfg, args, output); err != nil {
		return err
	}
	if dryRun {
		return nil
	}
	args = deckArgs(cfg, "sync", state)
	args = append(args, "--yes")
	return runDeckTool(ctx, cfg, args, output)
}

func runDeckTool(ctx context.Context, cfg config.Config, args []string, output io.Writer) error {
	tokenFile, err := os.CreateTemp("", "kong-loadgen-konnect-token-*")
	if err != nil {
		return fmt.Errorf("create temporary token file: %w", err)
	}
	tokenPath := tokenFile.Name()
	defer os.Remove(tokenPath)
	if err := tokenFile.Chmod(0o600); err != nil {
		tokenFile.Close()
		return fmt.Errorf("secure temporary token file: %w", err)
	}
	if _, err := tokenFile.WriteString(cfg.Konnect.Token); err != nil {
		tokenFile.Close()
		return fmt.Errorf("write temporary token file: %w", err)
	}
	if err := tokenFile.Close(); err != nil {
		return fmt.Errorf("close temporary token file: %w", err)
	}
	globalFlags := []string{"--konnect-token-file", tokenPath}
	args = append(args[:4], append(globalFlags, args[4:]...)...)
	return runTool(ctx, "deck", args, "", "", output)
}

func deckArgs(cfg config.Config, operation, state string) []string {
	return []string{
		"--konnect-addr", regionBaseURL(cfg.Konnect.Region),
		"--konnect-control-plane-name", cfg.Konnect.ControlPlane,
		"gateway", operation,
		"--select-tag", ManagedTag,
		state,
	}
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

func runTool(ctx context.Context, name string, args []string, tokenVariable, token string, output io.Writer) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Env = os.Environ()
	if tokenVariable != "" {
		command.Env = withEnvironment(command.Env, tokenVariable, token)
	}
	result, err := command.CombinedOutput()
	text := string(result)
	if token != "" {
		text = strings.ReplaceAll(text, token, "[REDACTED]")
	}
	if text != "" {
		fmt.Fprint(output, text)
	}
	if err != nil {
		return fmt.Errorf("%s command failed: %w", name, err)
	}
	return nil
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
