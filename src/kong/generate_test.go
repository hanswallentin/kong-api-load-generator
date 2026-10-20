package kong

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kong/kong-loadgen/src/config"
)

func TestGenerateDeckStateGolden(t *testing.T) {
	cfg := config.Config{
		Konnect:  config.KonnectConfig{Tool: "kongctl", ControlPlane: "demo", CreateControlPlane: true, Tags: []string{"demo"}},
		Upstream: config.UpstreamConfig{DefaultURL: "http://localhost:9000"},
		Users: config.UsersConfig{
			KeyHeader: "apikey",
			Groups:    []config.UserGroup{{Name: "mobile-app", Count: 2, KeyPrefix: "mobile"}},
		},
		Endpoints: []config.Endpoint{{
			Name:    "orders-api",
			Path:    "/orders",
			Methods: []string{"GET", "POST"},
			Auth:    "key-auth",
			Plugins: []config.Plugin{{Name: "rate-limiting", Config: map[string]any{"minute": 60}}},
		}},
	}
	generated, err := Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	assertGolden(t, "deck.golden.yaml", generated.DeckState)
	assertGolden(t, "kongctl.golden.yaml", generated.KongctlConfig)
	assertGolden(t, "control-plane.golden.yaml", generated.ControlPlane)
}

func TestGenerateAddsIsolationAndGroupTagsToEveryEntity(t *testing.T) {
	cfg := config.Config{
		Konnect:  config.KonnectConfig{ControlPlane: "demo", Tags: []string{"team-demo"}},
		Upstream: config.UpstreamConfig{DefaultURL: "http://localhost:9000"},
		Users:    config.UsersConfig{Groups: []config.UserGroup{{Name: "mobile-app", Count: 1, KeyPrefix: "mobile"}}},
		Endpoints: []config.Endpoint{{
			Name: "catalog",
			Path: "/catalog",
			Auth: "none",
		}},
	}
	generated, err := Generate(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(generated.DeckState)
	for _, expected := range []string{"kong-loadgen", "team-demo", "mobile-app", "mobile-01"} {
		if !strings.Contains(text, expected) {
			t.Errorf("generated state does not contain %q", expected)
		}
	}
	if strings.Contains(text, "key-auth") {
		t.Fatal("auth: none must not generate a key-auth plugin")
	}
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	// UPDATE_GOLDEN=1 rewrites the golden files after the change has been reviewed.
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(got)) != strings.TrimSpace(string(want)) {
		t.Errorf("%s mismatch\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}
