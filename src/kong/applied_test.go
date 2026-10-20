package kong

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/kong/kong-loadgen/src/config"
)

func driftConfig() config.Config {
	return config.Config{
		Konnect:  config.KonnectConfig{Tool: "kongctl", ControlPlane: "demo", CreateControlPlane: true, Tags: []string{"demo"}},
		Upstream: config.UpstreamConfig{DefaultURL: "http://localhost:9000"},
		Users: config.UsersConfig{
			KeyHeader: "apikey",
			Groups:    []config.UserGroup{{Name: "mobile-app", Count: 2, KeyPrefix: "mobile"}},
		},
		Endpoints: []config.Endpoint{{
			Name:      "orders",
			Path:      "/orders",
			Frequency: 10,
			Auth:      "key-auth",
			Plugins:   []config.Plugin{{Name: "rate-limiting", Config: map[string]any{"minute": 60}}},
		}},
	}
}

// pushed records a fingerprint the way a successful configure does, in a temp config directory.
func pushed(t *testing.T, cfg config.Config) string {
	t.Helper()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.MkdirAll(generatedDirectory(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	parts, err := Fingerprint(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := recordApplied(generatedDirectory(configPath), parts); err != nil {
		t.Fatal(err)
	}
	return configPath
}

func TestDriftReportsNeverConfigured(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	drift, err := CheckDrift(driftConfig(), configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !drift.NeverConfigured {
		t.Fatalf("drift = %+v, want NeverConfigured", drift)
	}
}

func TestDriftIsCleanAfterPush(t *testing.T) {
	cfg := driftConfig()
	drift, err := CheckDrift(cfg, pushed(t, cfg))
	if err != nil {
		t.Fatal(err)
	}
	if !drift.Clean() {
		t.Fatalf("drift = %+v, want clean after configure", drift)
	}
}

func TestDriftNamesOnlyChangedParts(t *testing.T) {
	cfg := driftConfig()
	configPath := pushed(t, cfg)

	changedGroup := driftConfig()
	changedGroup.Users.Groups[0].Count = 3
	drift, err := CheckDrift(changedGroup, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(drift.Changed, []string{"consumers and API keys"}) {
		t.Fatalf("changed parts = %v, want consumers and API keys only", drift.Changed)
	}

	changedPlugin := driftConfig()
	changedPlugin.Endpoints[0].Plugins[0].Config = map[string]any{"minute": 600}
	drift, err = CheckDrift(changedPlugin, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(drift.Changed, []string{"plugins"}) {
		t.Fatalf("changed parts = %v, want plugins only", drift.Changed)
	}
}

func TestLoadSettingsAreNotKonnectDrift(t *testing.T) {
	cfg := driftConfig()
	configPath := pushed(t, cfg)

	cfg.Endpoints[0].Frequency = 50
	cfg.Endpoints[0].ErrorPercentage = 25
	drift, err := CheckDrift(cfg, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !drift.Clean() {
		t.Fatalf("drift = %+v; frequency and error settings never reach Konnect", drift)
	}
}

func TestClearAppliedMakesConfigurationUnconfigured(t *testing.T) {
	cfg := driftConfig()
	configPath := pushed(t, cfg)
	if err := clearApplied(generatedDirectory(configPath)); err != nil {
		t.Fatal(err)
	}
	drift, err := CheckDrift(cfg, configPath)
	if err != nil {
		t.Fatal(err)
	}
	if !drift.NeverConfigured {
		t.Fatalf("drift = %+v, want NeverConfigured after teardown", drift)
	}
}
