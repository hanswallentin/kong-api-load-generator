package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/kong/kong-loadgen/src/apis"
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
		Use:   "kong-loadgen",
		Short: "Config-driven Kong demo traffic generator",
		Long: `kong-loadgen configures Kong Konnect from one YAML file, then sends demo traffic
through the Kong gateway as simulated users and user groups, so Konnect Analytics
shows the traffic broken down per consumer.

Quick start:
  export KONNECT_TOKEN=<your Konnect personal access token>
  kong-loadgen validate                  check config.yaml and report problems with line numbers
  kong-loadgen configure --dry-run       preview the changes to Konnect
  kong-loadgen configure                 create the control plane, routes, plugins, consumers and API keys
  kong-loadgen upstream                  start the built-in mock backend (in a second terminal)
  kong-loadgen run                       send traffic; saving config.yaml while it runs applies changes live

config.yaml is read from beside the binary unless --config is given. The Konnect
token comes from KONNECT_TOKEN and is never written to files or output. The API
keys in the sample configuration are throwaway demo values.

Documentation is in docs/ (usage, configuration, demo playbook, troubleshooting).`,
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
		Use: "validate",
		Long: `Reads config.yaml and reports every problem with its line number, such as a
duplicate endpoint, an identity that names an unknown user, or a frequency of zero.
Exits non-zero when there are problems. Requires KONNECT_TOKEN, as configure does.`,
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
		Use: "configure",
		Long: `Generates the Kong configuration into generated/ and synchronises it to the control
plane named in konnect.control_plane: the control plane (created when
create_control_plane is true), services, routes, plugins, consumers and API keys.

Prints the planned changes first. Running it again changes nothing, and removing an
endpoint, group or user from config.yaml and running it again removes it from Konnect.

Examples:
  kong-loadgen configure --dry-run      show the planned changes and stop
  kong-loadgen configure --generate-only   write generated/ without contacting Konnect
  kong-loadgen configure                apply the changes
  kong-loadgen configure --with-apis    also create one Konnect API per endpoint, from apis.yaml`,
		Short: "Generate and synchronize tagged Kong resources to Konnect",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			generateOnly, _ := cmd.Flags().GetBool("generate-only")
			// Writing files needs no Konnect access, so the token is only required when pushing.
			path, cfg, err := readValidatedConfig(cmd, !generateOnly)
			if err != nil {
				return err
			}
			dryRun, _ := cmd.Flags().GetBool("dry-run")
			withAPIs := apisRequested(cmd)
			var items []apis.API
			if withAPIs {
				// Read the APIs file before anything changes, so a mistake in it costs nothing.
				if items, err = readAPIs(cmd, cfg, apisPath(cmd, path)); err != nil {
					return err
				}
			}
			if err := kong.Configure(cmd.Context(), cfg, path, dryRun, generateOnly, cmd.OutOrStdout()); err != nil {
				return err
			}
			if !withAPIs {
				return nil
			}
			return kong.ConfigureAPIs(cmd.Context(), cfg, path, items, dryRun, generateOnly, cmd.OutOrStdout())
		},
	}
	configure.Flags().Bool("dry-run", false, "show planned changes without applying them")
	configure.Flags().Bool("generate-only", false, "write generated files without contacting Konnect")
	addAPIFlags(configure, true)
	root.AddCommand(configure)
	teardown := &cobra.Command{
		Use: "teardown",
		Long: `Removes only the Gateway entities tagged with konnect.tags, and deletes the control
plane when this tool created it (it carries the managed-by: kong-loadgen label).
It shows what it will delete and asks for confirmation. Use --yes to skip the prompt.

With --with-apis it also deletes the Konnect APIs named after the endpoints, but only
those that carry the managed-by: kong-loadgen label. They are deleted before the
gateway entities, and one confirmation covers everything.`,
		Short: "Remove Kong resources managed by this configuration",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, cfg, err := readValidatedConfig(cmd, true)
			if err != nil {
				return err
			}
			assumeYes, _ := cmd.Flags().GetBool("yes")
			opts := kong.TeardownOptions{AssumeYes: assumeYes}
			if apisRequested(cmd) {
				for _, endpoint := range cfg.Endpoints {
					opts.APINames = append(opts.APINames, endpoint.Name)
				}
			}
			return kong.Teardown(cmd.Context(), cfg, path, opts, cmd.OutOrStdout(), cmd.InOrStdin())
		},
	}
	teardown.Flags().Bool("yes", false, "delete without asking for confirmation")
	addAPIFlags(teardown, false)
	root.AddCommand(teardown)
	root.AddCommand(newAPIsCommand())

	run := &cobra.Command{
		Use: "run",
		Long: `Sends traffic to gateway.proxy_url for every endpoint, at the configured frequency and
error percentage, as the configured users. It does not contact Konnect, so no token is
needed. Stop with Ctrl+C to print the summary.

While it runs, saving config.yaml or sending SIGHUP (kill -HUP <pid>) applies changes
to rates, error percentages, identities, and added or removed endpoints. Changes that
need configure are reported as warnings.`,
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
		Use: "upstream",
		Long: `Starts the built-in mock backend on the port in upstream.default_url. It answers for
the configured paths, enforces the API key on key-auth paths, honours X-Loadgen-Error
and latency headers, and echoes request headers, so the backend can show which consumer
reached it. It listens on all interfaces so a containerised data plane can reach it.
The mock is for demos: it echoes headers, so do not expose it to untrusted networks.`,
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

// addAPIFlags adds the flags that switch on the Konnect APIs. readsFile is true
// for commands that read the APIs file; teardown names the APIs from config.yaml.
func addAPIFlags(command *cobra.Command, readsFile bool) {
	command.Flags().Bool("with-apis", false, "include one Konnect API per endpoint, described in apis.yaml")
	if readsFile {
		command.Flags().String("apis-file", "", "APIs file to read (implies --with-apis; defaults to apis.yaml beside config.yaml)")
	}
}

// apisRequested reports whether the APIs are wanted. Giving --apis-file counts as
// asking for them, so a path is never silently ignored.
func apisRequested(cmd *cobra.Command) bool {
	withAPIs, _ := cmd.Flags().GetBool("with-apis")
	return withAPIs || cmd.Flags().Changed("apis-file")
}

// apisPath returns the APIs file: --apis-file if given, else apis.yaml beside the config.
func apisPath(cmd *cobra.Command, configPath string) string {
	if path, _ := cmd.Flags().GetString("apis-file"); path != "" {
		return path
	}
	return filepath.Join(filepath.Dir(configPath), apis.DefaultFile)
}

// readAPIs reads and validates the APIs file, printing problems with line numbers.
// Endpoints without an entry are filled from the generated template, and reported.
func readAPIs(cmd *cobra.Command, cfg config.Config, path string) ([]apis.API, error) {
	file, root, err := apis.Read(path)
	if err != nil {
		return nil, err
	}
	problems := apis.Validate(cfg, file, root)
	if len(problems) > 0 {
		for _, problem := range problems {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %s\n", path, problem.Error())
		}
		return nil, fmt.Errorf("%s has %d problem(s)", path, len(problems))
	}
	items, notes := apis.Resolve(cfg, file)
	for _, note := range notes {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %s\n", note)
	}
	return items, nil
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

// newAPIsCommand groups the commands that work with apis.yaml.
func newAPIsCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "apis",
		Short: "Work with the APIs file used by --with-apis",
		Args:  cobra.NoArgs,
	}
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Write apis.yaml with a description and spec template for every endpoint",
		Long: `Writes apis.yaml with a generated description and OpenAPI spec for every endpoint in
config.yaml. Edit the file afterwards, then run configure --with-apis. Refuses to overwrite
an existing file unless --force is given.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			path, cfg, err := readValidatedConfig(cmd, false)
			if err != nil {
				return err
			}
			target := apisPath(cmd, path)
			force, _ := cmd.Flags().GetBool("force")
			if _, err := os.Stat(target); err == nil && !force {
				return fmt.Errorf("%s already exists; edit it, or pass --force to replace it", target)
			}
			file := apis.File{}
			for _, endpoint := range cfg.Endpoints {
				description, spec := apis.Template(endpoint)
				file.APIs = append(file.APIs, apis.Entry{Endpoint: endpoint.Name, Description: description, Spec: spec})
			}
			var buffer bytes.Buffer
			encoder := yaml.NewEncoder(&buffer)
			encoder.SetIndent(2)
			if err := encoder.Encode(file); err != nil {
				return fmt.Errorf("encode %s: %w", target, err)
			}
			if err := encoder.Close(); err != nil {
				return fmt.Errorf("encode %s: %w", target, err)
			}
			data := buffer.Bytes()
			header := "# APIs created in Konnect by configure --with-apis, one per endpoint.\n" +
				"# Edit the description and spec freely. The API is named after its endpoint.\n"
			if err := os.WriteFile(target, append([]byte(header), data...), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", target, err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Wrote %d API(s) to %s\n", len(file.APIs), target)
			return nil
		},
	}
	initCmd.Flags().Bool("force", false, "replace an existing apis.yaml")
	initCmd.Flags().String("apis-file", "", "where to write the file (defaults to apis.yaml beside config.yaml)")
	command.AddCommand(initCmd)
	return command
}
