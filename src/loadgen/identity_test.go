package loadgen

import (
	"math"
	"testing"

	"github.com/kong/kong-loadgen/src/config"
)

var testGroups = []config.UserGroup{
	{Name: "mobile-app", Count: 10, KeyPrefix: "mobile"},
	{Name: "partner-portal", Count: 3, KeyPrefix: "partner"},
}

func TestWeightedGroupsMatchConfiguredShares(t *testing.T) {
	identity := &config.IdentityConfig{
		Mode: "weighted-groups",
		Groups: []config.IdentityGroup{
			{Group: "mobile-app", Weight: 70},
			{Group: "partner-portal", Weight: 30},
		},
	}
	selector, err := newSelector(identity, testGroups)
	if err != nil {
		t.Fatal(err)
	}
	src := newSource(1)
	const samples = 200000
	groupCounts := map[string]int{}
	userCounts := map[string]int{}
	for i := 0; i < samples; i++ {
		picked := selector.pick(src)
		groupCounts[picked.Bucket]++
		userCounts[picked.Username]++
	}
	assertShare(t, "mobile-app", groupCounts["mobile-app"], samples, 0.70)
	assertShare(t, "partner-portal", groupCounts["partner-portal"], samples, 0.30)
	// Within a group, every user should get roughly the same share.
	for index := 1; index <= 10; index++ {
		username := testGroups[0].Username(index)
		assertShare(t, username, userCounts[username], samples, 0.70/10)
	}
}

func TestWeightedGroupsNormaliseWeightsThatDoNotSumTo100(t *testing.T) {
	identity := &config.IdentityConfig{
		Mode: "weighted-groups",
		Groups: []config.IdentityGroup{
			{Group: "mobile-app", Weight: 7},
			{Group: "partner-portal", Weight: 3},
		},
	}
	selector, err := newSelector(identity, testGroups)
	if err != nil {
		t.Fatal(err)
	}
	src := newSource(2)
	const samples = 100000
	count := 0
	for i := 0; i < samples; i++ {
		if selector.pick(src).Bucket == "mobile-app" {
			count++
		}
	}
	assertShare(t, "mobile-app", count, samples, 0.70)
}

func TestRoundRobinIsPerfectlyEven(t *testing.T) {
	identity := &config.IdentityConfig{
		Mode: "round-robin",
		Groups: []config.IdentityGroup{
			{Group: "mobile-app"},
			{Group: "partner-portal"},
		},
	}
	selector, err := newSelector(identity, testGroups)
	if err != nil {
		t.Fatal(err)
	}
	src := newSource(3)
	const cycles = 1000
	counts := map[string]int{}
	for i := 0; i < cycles*13; i++ {
		counts[selector.pick(src).Username]++
	}
	if len(counts) != 13 {
		t.Fatalf("round-robin used %d users, want 13", len(counts))
	}
	for username, count := range counts {
		if count != cycles {
			t.Fatalf("%s picked %d times, want exactly %d", username, count, cycles)
		}
	}
}

func TestSingleUserAlwaysUsesNamedUser(t *testing.T) {
	selector, err := newSelector(&config.IdentityConfig{Mode: "single-user", User: "mobile-app-03"}, testGroups)
	if err != nil {
		t.Fatal(err)
	}
	src := newSource(4)
	for i := 0; i < 100; i++ {
		got := selector.pick(src)
		if got.Username != "mobile-app-03" || got.Key != "mobile-03" || got.Bucket != "mobile-app" {
			t.Fatalf("single-user picked %+v", got)
		}
	}
}

func TestNewSelectorRejectsUnknownUser(t *testing.T) {
	_, err := newSelector(&config.IdentityConfig{Mode: "single-user", User: "mobile-app-30"}, testGroups)
	if err == nil {
		t.Fatal("expected an error for a user outside the group count")
	}
}

func TestNilIdentityIsAnonymous(t *testing.T) {
	selector, err := newSelector(nil, testGroups)
	if err != nil || selector != nil {
		t.Fatalf("selector = %v, err = %v; want nil selector for auth: none", selector, err)
	}
	if got := anonymous(); got.Bucket != BucketAnonymous || got.Key != "" {
		t.Fatalf("anonymous identity = %+v", got)
	}
}

// assertShare fails when the observed share is outside a tolerance of 6 standard
// errors, which keeps the test reliable while still catching a real skew.
func assertShare(t *testing.T, label string, count, samples int, want float64) {
	t.Helper()
	observed := float64(count) / float64(samples)
	tolerance := 6 * math.Sqrt(want*(1-want)/float64(samples))
	if math.Abs(observed-want) > tolerance {
		t.Fatalf("%s share = %.4f, want %.4f +/- %.4f", label, observed, want, tolerance)
	}
}
