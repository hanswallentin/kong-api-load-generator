package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kong/kong-loadgen/src/config"
	"github.com/kong/kong-loadgen/src/kong"
	"github.com/kong/kong-loadgen/src/loadgen"
	"github.com/kong/kong-loadgen/src/mock"
	"github.com/kong/kong-loadgen/src/watch"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

var version = "dev"

func main() {
	if err := newRootCommand().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "kong-loadgen",
		Short:         "Config-driven Kong demo traffic generator",
		Long:          "Configure Kong resources and generate attributable demo traffic from one YAML file.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.PersistentFlags().String("config", "", "path to config.yaml (defaults to the file beside the binary)")
	root.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version information",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Fprintln(cmd.OutOrStdout(), version)
		},
	})
	root.AddCommand(&cobra.Command{
		Use:   "validate",
		Short: "Validate configuration and report problems with line numbers",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, _, err := readValidatedConfig(cmd, true)
			if err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Configuration is valid: %s\n", path)
			return nil
		},
	})
	configure := &cobra.Command{
		Use:   "configure",
		Short: "Generate and synchronize tagged Kong resources to Konnect",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, cfg, err := readValidatedConfig(cmd, true)
			if err != nil {
				return err
			}
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			generateOnly, _ := cmd.Flags().GetBool("generate-only")
			return kong.Configure(cmd.Context(), cfg, path, dryRun, generateOnly, cmd.OutOrStdout())
		},
	}
	configure.Flags().Bool("dry-run", false, "show planned changes without applying them")
	configure.Flags().Bool("generate-only", false, "write generated files without contacting Konnect")
	root.AddCommand(configure)
	teardown := &cobra.Command{
		Use:   "teardown",
		Short: "Remove Kong resources managed by this configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, cfg, err := readValidatedConfig(cmd, true)
			if err != nil {
				return err
			}
			assumeYes, _ := cmd.Flags().GetBool("yes")
			return kong.Teardown(cmd.Context(), cfg, path, assumeYes, cmd.OutOrStdout(), cmd.InOrStdin())
		},
	}
	teardown.Flags().Bool("yes", false, "delete without asking for confirmation")
	root.AddCommand(teardown)

	run := &cobra.Command{
		Use:   "run",
		Short: "Generate load according to config.yaml",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, cfg, err := readValidatedConfig(cmd, false)
			if err != nil {
				return err
			}
			duration, _ := cmd.Flags().GetDuration("duration")
			jsonSummary, _ := cmd.Flags().GetString("json-summary")
			verbose, _ := cmd.Flags().GetBool("verbose-identity")
			seed, _ := cmd.Flags().GetUint64("seed")
			out := cmd.OutOrStdout()
			color := isTerminal(out) && os.Getenv("NO_COLOR") == ""

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			reload := reloadSignals(ctx, cmd, path)
			summary, err := loadgen.Run(ctx, cfg, loadgen.Options{
				Duration:        duration,
				Seed:            seed,
				VerboseIdentity: verbose,
				Live:            isTerminal(out),
				Color:           color,
				Out:             out,
				Reload:          reload,
				LoadConfig: func() (config.Config, error) {
					_, reloaded, err := readValidatedConfig(cmd, false)
					return reloaded, err
				},
				DriftCheck: func(current config.Config) []string {
					return driftNotes(current, path)
				},
			})
			if err != nil {
				return err
			}
			loadgen.PrintSummary(out, color, summary)
			if jsonSummary != "" {
				if err := writeJSONSummary(jsonSummary, summary); err != nil {
					return err
				}
				fmt.Fprintf(out, "Wrote JSON summary to %s\n", jsonSummary)
			}
			return nil
		},
	}
	run.Flags().Duration("duration", 0, "stop after this long, for example 10m (overrides load.duration; default runs until Ctrl+C)")
	run.Flags().String("json-summary", "", "write final statistics to this JSON file")
	run.Flags().Bool("verbose-identity", false, "break live statistics down per individual user, not just per group")
	run.Flags().Uint64("seed", 0, "random seed for repeatable runs (0 picks a random seed)")
	root.AddCommand(run)

	upstream := &cobra.Command{
		Use:   "upstream",
		Short: "Start the built-in mock upstream",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			_, cfg, err := readValidatedConfig(cmd, false)
			if err != nil {
				return err
			}
			listen, _ := cmd.Flags().GetString("listen")
			if listen == "" {
				if listen, err = mock.ListenAddress(cfg.Upstream.DefaultURL); err != nil {
					return err
				}
			}
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return mock.Serve(ctx, listen, cfg, cmd.OutOrStdout())
		},
	}
	upstream.Flags().String("listen", "", "address to listen on (defaults to the port in upstream.default_url, on all interfaces)")
	root.AddCommand(upstream)
	return root
}

// reloadSignals merges file changes and SIGHUP into one reload channel. A
// reload is requested with `kill -HUP <pid>` or by saving the config file.
func reloadSignals(ctx context.Context, cmd *cobra.Command, path string) <-chan struct{} {
	reload := make(chan struct{}, 1)
	request := func() {
		select {
		case reload <- struct{}{}:
		default:
		}
	}
	changes, err := watch.File(ctx, path)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: file watching unavailable (%v); send SIGHUP to reload\n", err)
	}
	hangup := make(chan os.Signal, 1)
	signal.Notify(hangup, syscall.SIGHUP)
	go func() {
		defer signal.Stop(hangup)
		for {
			select {
			case <-ctx.Done():
				return
			case <-changes:
				request()
			case <-hangup:
				request()
			}
		}
	}()
	return reload
}

// driftNotes explains what configure still has to push, or returns nil when Konnect matches.
func driftNotes(cfg config.Config, path string) []string {
	drift, err := kong.CheckDrift(cfg, path)
	if err != nil {
		return []string{fmt.Sprintf("could not compare with the last configure: %v", err)}
	}
	if drift.NeverConfigured {
		return []string{"configure has not been run for this configuration; run: kong-loadgen configure"}
	}
	if len(drift.Changed) > 0 {
		return []string{fmt.Sprintf("changed since the last configure: %s; run: kong-loadgen configure (traffic for these still goes to the old Konnect config)", strings.Join(drift.Changed, ", "))}
	}
	return nil
}

func isTerminal(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func writeJSONSummary(path string, summary loadgen.Summary) error {
	data, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return fmt.Errorf("encode summary: %w", err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write summary: %w", err)
	}
	return nil
}

func readValidatedConfig(cmd *cobra.Command, requireToken bool) (string, config.Config, error) {
	path, err := configPath(cmd)
	if err != nil {
		return "", config.Config{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", config.Config{}, fmt.Errorf("read config %q: %w", path, err)
	}
	var document yaml.Node
	if err := yaml.Unmarshal(data, &document); err != nil {
		return "", config.Config{}, err
	}
	var cfg config.Config
	if err := document.Decode(&cfg); err != nil {
		return "", config.Config{}, err
	}
	config.ApplyEnvironment(&cfg)
	var problems []config.ValidationError
	if requireToken {
		problems = config.Validate(cfg, documentRoot(&document))
	} else {
		problems = config.ValidateLocal(cfg, documentRoot(&document))
	}
	if len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintln(cmd.ErrOrStderr(), problem.Error())
		}
		return "", config.Config{}, fmt.Errorf("configuration has %d problem(s)", len(problems))
	}
	return path, cfg, nil
}

func configPath(cmd *cobra.Command) (string, error) {
	path, _ := cmd.Flags().GetString("config")
	if path != "" {
		return path, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate executable: %w", err)
	}
	return filepath.Join(filepath.Dir(executable), "config.yaml"), nil
}

func documentRoot(document *yaml.Node) *yaml.Node {
	if document != nil && len(document.Content) > 0 {
		return document.Content[0]
	}
	return document
}
