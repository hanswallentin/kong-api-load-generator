package apis

import (
	"strings"
	"testing"

	"github.com/kong/kong-loadgen/src/config"
	"gopkg.in/yaml.v3"
)

func validateSource(t *testing.T, cfg config.Config, source string) []config.ValidationError {
	t.Helper()
	var document yaml.Node
	if err := yaml.Unmarshal([]byte(source), &document); err != nil {
		t.Fatal(err)
	}
	var file File
	if err := document.Decode(&file); err != nil {
		t.Fatal(err)
	}
	return Validate(cfg, file, documentRoot(&document))
}

var testConfig = config.Config{Endpoints: []config.Endpoint{{Name: "orders-api", Path: "/orders", Methods: []string{"GET"}}}}

func TestValidateAcceptsCompleteEntry(t *testing.T) {
	source := "apis:\n  - endpoint: orders-api\n    description: Orders.\n    spec: |\n      openapi: 3.0.3\n"
	if problems := validateSource(t, testConfig, source); len(problems) != 0 {
		t.Fatalf("unexpected problems: %v", problems)
	}
}

func TestValidateReportsUnknownEndpointWithLine(t *testing.T) {
	source := "apis:\n  - endpoint: no-such-endpoint\n    description: Orders.\n    spec: |\n      openapi: 3.0.3\n"
	problems := validateSource(t, testConfig, source)
	if len(problems) != 1 || problems[0].Line != 2 || !strings.Contains(problems[0].Message, "not defined in config.yaml") {
		t.Fatalf("problems = %v, want one unknown-endpoint problem on line 2", problems)
	}
}

func TestValidateRequiresDescriptionAndSpec(t *testing.T) {
	source := "apis:\n  - endpoint: orders-api\n    description: \"\"\n    spec: \"\"\n"
	problems := validateSource(t, testConfig, source)
	if len(problems) != 2 {
		t.Fatalf("problems = %v, want description and spec problems", problems)
	}
}

func TestValidateReportsDuplicateEndpoint(t *testing.T) {
	source := "apis:\n  - endpoint: orders-api\n    description: A.\n    spec: |\n      openapi: 3.0.3\n  - endpoint: orders-api\n    description: B.\n    spec: |\n      openapi: 3.0.3\n"
	problems := validateSource(t, testConfig, source)
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "duplicate API") {
		t.Fatalf("problems = %v, want one duplicate problem", problems)
	}
}

func TestValidateRejectsSpecThatIsNotYAML(t *testing.T) {
	source := "apis:\n  - endpoint: orders-api\n    description: Orders.\n    spec: \"a: [unclosed\"\n"
	problems := validateSource(t, testConfig, source)
	if len(problems) != 1 || !strings.Contains(problems[0].Message, "not valid YAML") {
		t.Fatalf("problems = %v, want invalid YAML problem", problems)
	}
}
