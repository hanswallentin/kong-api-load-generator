package loadgen

import (
	"bytes"
	"strings"
	"testing"
)

var consoleSummary = Summary{
	DurationSeconds: 15,
	Totals:          Totals{Requests: 300, Errors: 30, ActualRPS: 20, ErrorPct: 10},
	Endpoints: []EndpointStat{
		{Name: "orders-api", TargetRPS: 20, ActualRPS: 19.9, Errors: 30, ErrorPct: 10, Status2xx: 240, Status5xx: 20, Status4xx: 40, Status429: 3, P50Ms: 40, P95Ms: 90, P99Ms: 120, Requests: 300},
	},
	Buckets: []BucketStat{{Name: "mobile-app", Requests: 300, SharePct: 100, ErrorPct: 10}},
}

func TestConsoleHasNoEscapeCodesWhenColourIsOff(t *testing.T) {
	var out bytes.Buffer
	PrintSummary(&out, false, consoleSummary)
	if strings.Contains(out.String(), "\x1b[") {
		t.Fatalf("escape codes written with colour off:\n%q", out.String())
	}
	if !strings.Contains(out.String(), "orders-api") {
		t.Fatalf("summary missing endpoint row:\n%s", out.String())
	}
}

func TestConsoleColoursErrorsWhenColourIsOn(t *testing.T) {
	var out bytes.Buffer
	PrintSummary(&out, true, consoleSummary)
	if !strings.Contains(out.String(), "\x1b[31m20") {
		t.Fatalf("expected 5xx count in red when colour is on:\n%q", out.String())
	}
}

func TestColouredColumnsStayAligned(t *testing.T) {
	plainOut := &bytes.Buffer{}
	coloured := &bytes.Buffer{}
	PrintSummary(plainOut, false, consoleSummary)
	PrintSummary(coloured, true, consoleSummary)
	stripped := stripEscapes(coloured.String())
	if stripped != plainOut.String() {
		t.Fatalf("coloured output differs from plain output after removing escape codes\nplain:\n%s\ncoloured (stripped):\n%s", plainOut.String(), stripped)
	}
}

func stripEscapes(text string) string {
	var out strings.Builder
	for i := 0; i < len(text); i++ {
		if text[i] == 0x1b {
			for i < len(text) && text[i] != 'm' {
				i++
			}
			continue
		}
		out.WriteByte(text[i])
	}
	return out.String()
}
