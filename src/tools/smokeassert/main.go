// Command smokeassert checks a run's JSON summary against the configuration
// that produced it. It is used by `make smoke`.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/kong/kong-loadgen/src/config"
	"github.com/kong/kong-loadgen/src/loadgen"
)

func main() {
	summaryPath := flag.String("summary", "", "JSON summary written by kong-loadgen run")
	configPath := flag.String("config", "", "configuration the run used")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		fail("read config: %v", err)
	}
	data, err := os.ReadFile(*summaryPath)
	if err != nil {
		fail("read summary: %v", err)
	}
	var summary loadgen.Summary
	if err := json.Unmarshal(data, &summary); err != nil {
		fail("parse summary: %v", err)
	}

	failures := check(cfg, summary)
	if len(failures) > 0 {
		for _, failure := range failures {
			fmt.Fprintln(os.Stderr, "FAIL:", failure)
		}
		fmt.Fprintln(os.Stderr, "SMOKE FAILED")
		os.Exit(1)
	}
	fmt.Printf("SMOKE PASSED: %d requests over %.1fs, %.1f rps, %.1f%% errors\n",
		summary.Totals.Requests, summary.DurationSeconds, summary.Totals.ActualRPS, summary.Totals.ErrorPct)
}

// check compares the run with what the configuration predicts. The bucket
// model assumes error_mode mixed against the built-in mock, which is what the
// smoke configuration uses. For a key-auth endpoint with error rate p, the
// expected shares are: upstream 0.5p and malformed 0.125p (routed to the user),
// missing key 0.125p and unknown path 0.125p (Anonymous), wrong key 0.125p
// (Invalid-credentials). Auth none endpoints are all Anonymous.
func check(cfg config.Config, summary loadgen.Summary) []string {
	var failures []string
	endpoints := map[string]loadgen.EndpointStat{}
	for _, stat := range summary.Endpoints {
		endpoints[stat.Name] = stat
	}
	rampSeconds := cfg.Load.RampUp.Value().Seconds()
	// Ramp-up lowers the average rate, so compare against the ramped window.
	effectiveSeconds := summary.DurationSeconds - rampSeconds/2

	expectedBuckets := map[string]float64{}
	var total float64
	for _, endpoint := range cfg.Endpoints {
		stat, ok := endpoints[endpoint.Name]
		if !ok {
			failures = append(failures, fmt.Sprintf("endpoint %s has no statistics", endpoint.Name))
			continue
		}
		requests := float64(stat.Requests)
		total += requests

		expectedRequests := endpoint.RequestsPerSecond() * effectiveSeconds
		if math.Abs(requests-expectedRequests)/expectedRequests > 0.10 {
			failures = append(failures, fmt.Sprintf("%s: %.0f requests, expected %.0f (+/-10%%)", endpoint.Name, requests, expectedRequests))
		}

		p := endpoint.ErrorPercentage / 100
		if !withinSigmas(stat.ErrorPct, endpoint.ErrorPercentage, requests, p) {
			failures = append(failures, fmt.Sprintf("%s: error rate %.2f%%, expected %.2f%%", endpoint.Name, stat.ErrorPct, endpoint.ErrorPercentage))
		}
		if stat.Transport > 0 || stat.Dropped > 0 {
			failures = append(failures, fmt.Sprintf("%s: %d transport errors and %d dropped requests (both should be 0)", endpoint.Name, stat.Transport, stat.Dropped))
		}

		if endpoint.Auth == "none" {
			expectedBuckets[loadgen.BucketAnonymous] += requests
			continue
		}
		expectedBuckets[loadgen.BucketAnonymous] += requests * 0.25 * p
		expectedBuckets[loadgen.BucketInvalid] += requests * 0.125 * p
		routed := requests * (1 - 0.375*p)
		for group, share := range groupShares(endpoint.Identity, cfg.Users.Groups) {
			expectedBuckets[group] += routed * share
		}
	}

	observed := map[string]int64{}
	for _, bucket := range summary.Buckets {
		observed[bucket.Name] = bucket.Requests
	}
	for name, count := range observed {
		if _, expected := expectedBuckets[name]; !expected && count > 0 {
			failures = append(failures, fmt.Sprintf("unexpected bucket %q with %d requests", name, count))
		}
	}
	for name, expected := range expectedBuckets {
		share := expected / total
		if !withinSigmas(float64(observed[name])*100/float64(summary.Totals.Requests), share*100, float64(summary.Totals.Requests), share) {
			failures = append(failures, fmt.Sprintf("bucket %s: %d requests, expected about %.0f", name, observed[name], expected))
		}
	}
	return failures
}

// groupShares returns each group's share of the requests routed to users.
func groupShares(identity *config.IdentityConfig, groups []config.UserGroup) map[string]float64 {
	shares := map[string]float64{}
	if identity == nil {
		return shares
	}
	counts := map[string]int{}
	for _, group := range groups {
		counts[group.Name] = group.Count
	}
	switch identity.Mode {
	case "single-user":
		group, _, _ := config.FindUser(groups, identity.User)
		shares[group.Name] = 1
	case "weighted-groups":
		var sum float64
		for _, ref := range identity.Groups {
			sum += float64(ref.Weight)
		}
		for _, ref := range identity.Groups {
			shares[ref.Group] = float64(ref.Weight) / sum
		}
	case "round-robin":
		var users float64
		for _, ref := range identity.Groups {
			users += float64(counts[ref.Group])
		}
		for _, ref := range identity.Groups {
			shares[ref.Group] = float64(counts[ref.Group]) / users
		}
	}
	return shares
}

// withinSigmas accepts an observed percentage when it is within 4 standard
// errors of the expected probability, with a floor of 0.5 percentage points.
// want is in percent; probability is the same value as a fraction.
func withinSigmas(observedPct, wantPct, samples, probability float64) bool {
	if samples <= 0 {
		return false
	}
	sigma := math.Sqrt(probability*(1-probability)/samples) * 100
	tolerance := math.Max(4*sigma, 0.5)
	return math.Abs(observedPct-wantPct) <= tolerance
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "smokeassert: "+format+"\n", args...)
	os.Exit(1)
}
