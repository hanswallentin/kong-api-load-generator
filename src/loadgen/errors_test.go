package loadgen

import "testing"

const errorSamples = 200000

func countKinds(plan errorPlan, samples int) map[errorKind]int {
	src := newSource(7)
	counts := map[errorKind]int{}
	for i := 0; i < samples; i++ {
		counts[chooseError(src, plan)]++
	}
	return counts
}

func TestObservedErrorRateMatchesConfiguredPercentage(t *testing.T) {
	for _, percentage := range []float64{0, 5, 10, 50, 100} {
		counts := countKinds(errorPlan{percentage: percentage, mode: "mixed", builtin: true, keyAuth: true}, errorSamples)
		failed := errorSamples - counts[noError]
		want := percentage / 100
		assertShare(t, "error rate", failed, errorSamples, want)
	}
}

func TestMixedModeSplitsUpstreamAndClientErrors(t *testing.T) {
	counts := countKinds(errorPlan{percentage: 100, mode: "mixed", builtin: true, keyAuth: true}, errorSamples)
	assertShare(t, "upstream within mixed", counts[upstreamError], errorSamples, 0.5)
	clientErrors := errorSamples - counts[upstreamError]
	for _, kind := range []errorKind{missingKey, wrongKey, unknownPath, malformedBody} {
		assertShare(t, "client kind within mixed", counts[kind], clientErrors, 0.25)
	}
}

func TestUpstreamModeFallsBackToClientWithoutBuiltinMock(t *testing.T) {
	counts := countKinds(errorPlan{percentage: 100, mode: "upstream", builtin: false, keyAuth: true}, 10000)
	if counts[upstreamError] != 0 {
		t.Fatalf("upstream errors = %d, want 0 when the upstream is not the built-in mock", counts[upstreamError])
	}
}

func TestAuthNoneNeverSendsKeyErrors(t *testing.T) {
	counts := countKinds(errorPlan{percentage: 100, mode: "client", builtin: true, keyAuth: false}, 20000)
	if counts[missingKey] != 0 || counts[wrongKey] != 0 {
		t.Fatalf("key-auth errors on an auth: none endpoint: missing=%d wrong=%d", counts[missingKey], counts[wrongKey])
	}
	if counts[unknownPath] == 0 || counts[malformedBody] == 0 {
		t.Fatalf("expected 404 and 400 variants, got %v", counts)
	}
}

func TestZeroPercentNeverFails(t *testing.T) {
	counts := countKinds(errorPlan{percentage: 0, mode: "mixed", builtin: true, keyAuth: true}, 10000)
	if counts[noError] != 10000 {
		t.Fatalf("0%% endpoint produced %d errors", 10000-counts[noError])
	}
}
