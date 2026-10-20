package loadgen

// errorKind is the failure a single request is asked to produce.
type errorKind int

const (
	noError       errorKind = iota
	upstreamError           // X-Loadgen-Error asks the mock upstream for a 5xx
	missingKey              // no key on a key-auth endpoint: Kong returns 401
	wrongKey                // a key Kong does not know: 401, Invalid-credentials bucket
	unknownPath             // a path no route matches: Kong returns 404
	malformedBody           // invalid JSON to the upstream: 400
)

// errorPlan is the per-endpoint input to chooseError.
type errorPlan struct {
	percentage float64 // share of requests that should fail, 0-100
	mode       string  // upstream, client, or mixed
	builtin    bool    // true when the upstream is the built-in mock
	keyAuth    bool    // true when the endpoint enforces key-auth
}

// chooseError decides whether a request fails and, if so, how. Upstream
// errors need the built-in mock; otherwise they fall back to client errors.
func chooseError(src *source, plan errorPlan) errorKind {
	if src.float64()*100 >= plan.percentage {
		return noError
	}
	useUpstream := plan.builtin && (plan.mode == "upstream" || (plan.mode == "mixed" && src.intN(2) == 0))
	if useUpstream {
		return upstreamError
	}
	candidates := []errorKind{unknownPath, malformedBody}
	if plan.keyAuth {
		candidates = append(candidates, missingKey, wrongKey)
	}
	return candidates[src.intN(len(candidates))]
}
