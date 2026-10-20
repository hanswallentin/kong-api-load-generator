package config

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const validConfig = `konnect:
  tool: kongctl
  token: demo-token
  region: eu
  control_plane: demo
gateway:
  proxy_url: http://localhost:8000
upstream:
  default_url: http://localhost:9000
load:
  duration: 0
  ramp_up: 1s
  max_concurrency: 10
  jitter: 0.2
  client:
    timeout: 2s
users:
  key_header: apikey
  groups:
    - name: mobile-app
      count: 3
      key_prefix: mobile
endpoints:
  - name: orders
    path: /orders
    methods: [GET]
    frequency: 10
    error_percentage: 5
    auth: key-auth
    identity:
      mode: single-user
      user: mobile-app-03
  - name: catalog
    path: /catalog
    methods: [GET]
    frequency_per_minute: 60
    error_percentage: 0
    auth: none
`

func parseConfig(t *testing.T, source string) (Config, *yaml.Node) {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		t.Fatal(err)
	}
	var cfg Config
	if err := document.Decode(&cfg); err != nil {
		t.Fatal(err)
	}
	return cfg, document.Content[0]
}

func TestValidateAcceptsAPIKeyIdentityAndAnonymousEndpoint(t *testing.T) {
	cfg, root := parseConfig(t, validConfig)
	if problems := Validate(cfg, root); len(problems) != 0 {
		t.Fatalf("expected valid configuration, got: %v", problems)
	}
	if got := cfg.Endpoints[1].RequestsPerSecond(); got != 1 {
		t.Fatalf("frequency_per_minute conversion = %v, want 1", got)
	}
}

func TestValidateReportsKeyAuthIdentityMismatchWithLine(t *testing.T) {
	source := strings.Replace(validConfig, "    identity:\n      mode: single-user\n      user: mobile-app-03\n", "", 1)
	cfg, root := parseConfig(t, source)
	problems := Validate(cfg, root)
	for _, problem := range problems {
		if strings.Contains(problem.Message, "require an identity block") {
			if problem.Line != 24 {
				t.Fatalf("identity error line = %d, want endpoint line 24", problem.Line)
			}
			return
		}
	}
	t.Fatalf("expected missing identity problem, got: %v", problems)
}

func TestValidateRejectsDuplicateKeyPrefixes(t *testing.T) {
	source := strings.Replace(validConfig, "key_prefix: mobile\n", "key_prefix: shared\n", 1)
	source = strings.Replace(source, "    - name: mobile-app\n", "    - name: mobile-app\n      count: 3\n      key_prefix: shared\n    - name: other\n", 1)
	cfg, root := parseConfig(t, source)
	problems := Validate(cfg, root)
	for _, problem := range problems {
		if strings.Contains(problem.Message, "key_prefix values must be unique") {
			return
		}
	}
	t.Fatalf("expected duplicate key_prefix problem, got: %v", problems)
}

func TestValidateRequiresToken(t *testing.T) {
	cfg, root := parseConfig(t, strings.Replace(validConfig, "token: demo-token\n", "token: \"\"\n", 1))
	problems := Validate(cfg, root)
	for _, problem := range problems {
		if strings.Contains(problem.Message, "Konnect token is required") && problem.Line == 3 {
			return
		}
	}
	t.Fatalf("expected line-numbered missing token problem, got: %v", problems)
}
