package kong

import (
	"strings"
	"testing"
)

func TestRedactRemovesToken(t *testing.T) {
	got := redact("token secret-abc in output secret-abc", "secret-abc")
	if strings.Contains(got, "secret-abc") {
		t.Fatalf("token still present: %q", got)
	}
	if got != "token [REDACTED] in output [REDACTED]" {
		t.Fatalf("redact = %q", got)
	}
}

func TestRedactIgnoresEmptySecret(t *testing.T) {
	if got := redact("unchanged", ""); got != "unchanged" {
		t.Fatalf("redact with empty secret changed text: %q", got)
	}
}

func TestWithEnvironmentReplacesExistingKey(t *testing.T) {
	env := withEnvironment([]string{"A=1", "KONGCTL_DEFAULT_KONNECT_PAT=old", "B=2"}, "KONGCTL_DEFAULT_KONNECT_PAT", "new")
	count := 0
	for _, entry := range env {
		if strings.HasPrefix(entry, "KONGCTL_DEFAULT_KONNECT_PAT=") {
			count++
			if entry != "KONGCTL_DEFAULT_KONNECT_PAT=new" {
				t.Fatalf("entry = %q, want new value", entry)
			}
		}
	}
	if count != 1 {
		t.Fatalf("found %d entries, want exactly 1", count)
	}
}

func TestConfirmAcceptsOnlyYes(t *testing.T) {
	var output strings.Builder
	for input, want := range map[string]bool{"yes\n": true, "no\n": false, "\n": false, "YES\n": false} {
		got, err := confirm(strings.NewReader(input), &output)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("confirm(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestRequireToolsNamesMissingTool(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	err := requireTools("kongctl")
	if err == nil || !strings.Contains(err.Error(), `"kongctl"`) {
		t.Fatalf("requireTools error = %v, want missing kongctl message", err)
	}
}
