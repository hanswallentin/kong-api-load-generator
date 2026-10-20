package loadgen

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kong/kong-loadgen/src/config"
	"github.com/kong/kong-loadgen/src/mock"
)

// attributionFixture runs real requests against the mock upstream, so the
// status codes and buckets come from the same paths a demo uses.
func attributionFixture(t *testing.T) (*runner, map[string]*target) {
	t.Helper()
	server := httptest.NewServer(mock.NewHandler(attributionConfig()))
	t.Cleanup(server.Close)

	cfg := attributionConfig()
	cfg.Gateway.ProxyURL = server.URL
	cfg.Load.ErrorStatuses = []int{503}
	src := newSource(9)
	targets, err := buildTargets(cfg, src)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]*target{}
	for _, target := range targets {
		byName[target.name] = target
	}
	r := &runner{
		cfg:     cfg,
		src:     src,
		client:  newClient(5*time.Second, 4),
		stats:   newCollector(src, true),
		sem:     make(chan struct{}, 4),
		targets: byName,
		ctx:     context.Background(),
	}
	return r, byName
}

func attributionConfig() config.Config {
	return config.Config{
		Gateway:  config.GatewayConfig{ProxyURL: "http://unused"},
		Upstream: config.UpstreamConfig{DefaultURL: "http://localhost:9000"},
		Load:     config.LoadConfig{MaxConcurrency: 4, Client: config.ClientConfig{Timeout: config.Duration(5 * time.Second)}},
		Users: config.UsersConfig{
			KeyHeader: "apikey",
			Groups: []config.UserGroup{
				{Name: "mobile-app", Count: 3, KeyPrefix: "mobile"},
				{Name: "partner-portal", Count: 2, KeyPrefix: "partner"},
			},
		},
		Endpoints: []config.Endpoint{
			{
				Name: "orders", Path: "/orders", Methods: []string{"POST"}, Frequency: 1, Auth: "key-auth",
				Identity:        &config.IdentityConfig{Mode: "single-user", User: "mobile-app-03"},
				ErrorPercentage: 100, ErrorMode: "mixed",
			},
			{Name: "catalog", Path: "/catalog", Methods: []string{"GET"}, Frequency: 1, Auth: "none", ErrorPercentage: 100},
		},
	}
}

func TestEachRequestIsAttributedToExactlyOneBucket(t *testing.T) {
	r, targets := attributionFixture(t)
	orders, catalog := targets["orders"], targets["catalog"]

	// Each call performs one request of the given kind and records it.
	steps := []struct {
		target *target
		kind   errorKind
		bucket string
		status int
	}{
		{orders, noError, "mobile-app", http.StatusOK},
		{orders, upstreamError, "mobile-app", 503},
		{orders, malformedBody, "mobile-app", http.StatusBadRequest},
		{orders, missingKey, BucketAnonymous, http.StatusUnauthorized},
		{orders, wrongKey, BucketInvalid, http.StatusUnauthorized},
		{orders, unknownPath, BucketAnonymous, http.StatusNotFound},
		{catalog, noError, BucketAnonymous, http.StatusOK},
	}
	for _, step := range steps {
		r.perform(step.target, step.kind)
	}

	wantBuckets := map[string]counter{
		"mobile-app":    {Requests: 3, Errors: 2},
		BucketAnonymous: {Requests: 3, Errors: 2},
		BucketInvalid:   {Requests: 1, Errors: 1},
	}
	var total int64
	for name, want := range wantBuckets {
		got := r.stats.buckets[name]
		if got == nil || got.Requests != want.Requests || got.Errors != want.Errors {
			t.Fatalf("bucket %s = %+v, want %+v", name, got, want)
		}
		total += got.Requests
	}
	if total != int64(len(steps)) {
		t.Fatalf("buckets account for %d requests, want %d (each request counted once)", total, len(steps))
	}
}

func TestEachRequestHasExpectedStatus(t *testing.T) {
	r, targets := attributionFixture(t)
	orders := targets["orders"]
	r.perform(orders, missingKey)
	r.perform(orders, wrongKey)
	r.perform(orders, noError)

	stats := r.stats.endpoints["orders"]
	if stats.Status4xx != 2 || stats.Status2xx != 1 {
		t.Fatalf("status counts 4xx=%d 2xx=%d, want 4xx=2 2xx=1", stats.Status4xx, stats.Status2xx)
	}
	if r.stats.users["mobile-app-03"] == nil || r.stats.users["mobile-app-03"].Requests != 1 {
		t.Fatalf("per-user count for mobile-app-03 = %+v, want 1 authenticated request", r.stats.users["mobile-app-03"])
	}
}
