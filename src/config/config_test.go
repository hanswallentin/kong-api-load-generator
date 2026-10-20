package config

import "testing"

func TestApplyEnvironmentOverridesTokenAndRegion(t *testing.T) {
	t.Setenv("KONNECT_TOKEN", "env-token")
	t.Setenv("KONNECT_REGION", "au")
	cfg := Config{Konnect: KonnectConfig{Token: "file-token", Region: "eu"}}

	ApplyEnvironment(&cfg)

	if cfg.Konnect.Token != "env-token" {
		t.Fatalf("token = %q, want environment value", cfg.Konnect.Token)
	}
	if cfg.Konnect.Region != "au" {
		t.Fatalf("region = %q, want environment value", cfg.Konnect.Region)
	}
}

func TestApplyEnvironmentKeepsFileValuesWhenUnset(t *testing.T) {
	t.Setenv("KONNECT_TOKEN", "")
	t.Setenv("KONNECT_REGION", "")
	cfg := Config{Konnect: KonnectConfig{Token: "file-token", Region: "eu"}}

	ApplyEnvironment(&cfg)

	if cfg.Konnect.Token != "file-token" || cfg.Konnect.Region != "eu" {
		t.Fatalf("file values changed: %+v", cfg.Konnect)
	}
}
