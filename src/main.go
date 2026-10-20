package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/kong/kong-loadgen/src/config"
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
			path, err := configPath(cmd)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read config %q: %w", path, err)
			}
			var document yaml.Node
			if err := yaml.Unmarshal(data, &document); err != nil {
				return err
			}
			var cfg config.Config
			if err := document.Decode(&cfg); err != nil {
				return err
			}
			if cfg.Konnect.Token == "" {
				cfg.Konnect.Token = os.Getenv("KONNECT_TOKEN")
			}
			problems := config.Validate(cfg, documentRoot(&document))
			if len(problems) > 0 {
				for _, problem := range problems {
					fmt.Fprintln(cmd.ErrOrStderr(), problem.Error())
				}
				return fmt.Errorf("configuration has %d problem(s)", len(problems))
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Configuration is valid: %s\n", path)
			return nil
		},
	})
	return root
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
