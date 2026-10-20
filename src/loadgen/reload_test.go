package loadgen

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kong/kong-loadgen/src/config"
	"github.com/kong/kong-loadgen/src/mock"
)

func reloadBase(t *testing.T) (config.Config, *httptest.Server) {
	t.Helper()
	cfg := config.Config{
		Gateway:  config.GatewayConfig{ProxyURL: "http://placeholder"},
		Upstream: config.UpstreamConfig{DefaultURL: "http://localhost:9000"},
		Load:     config.LoadConfig{MaxConcurrency: 20, Client: config.ClientConfig{Timeout: config.Duration(5 * time.Second)}},
		Users: config.UsersConfig{
			KeyHeader: "apikey",
			Groups:    []config.UserGroup{{Name: "mobile-app", Count: 3, KeyPrefix: "mobile"}},
		},
		Endpoints: []config.Endpoint{
			{Name: "catalog", Path: "/catalog", Methods: []string{"GET"}, Frequency: 10, Auth: "none"},
			{
				Name: "orders", Path: "/orders", Methods: []string{"GET"}, Frequency: 5, Auth: "key-auth",
				ErrorPercentage: 0, Identity: &config.IdentityConfig{Mode: "single-user", User: "mobile-app-01"},
			},
		},
	}
	server := httptest.NewServer(mock.NewHandler(cfg))
	t.Cleanup(server.Close)
	cfg.Gateway.ProxyURL = server.URL
	return cfg, server
}

// startRunner starts schedulers for cfg without the reload loop, so tests can call reload directly.
func startRunner(t *testing.T, cfg config.Config) (*runner, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	src := newSource(21)
	r := &runner{
		cfg:     cfg,
		opts:    Options{Out: &out},
		src:     src,
		client:  newClient(5*time.Second, cfg.Load.MaxConcurrency),
		stats:   newCollector(src, false),
		sem:     make(chan struct{}, cfg.Load.MaxConcurrency),
		start:   time.Now(),
		ctx:     ctx,
		targets: map[string]*target{},
	}
	for _, endpoint := range cfg.Endpoints {
		s, _, err := newSettings(cfg, endpoint, src)
		if err != nil {
			t.Fatal(err)
		}
		tg := &target{name: endpoint.Name}
		tg.current.Store(s)
		r.targets[endpoint.Name] = tg
		r.order = append(r.order, endpoint.Name)
	}
	r.lockedStartAll()
	// Cleanups run last-in first-out: stop the schedulers, then wait for them.
	t.Cleanup(func() {
		cancel()
		r.schedulers.Wait()
	})
	return r, &out
}

func TestReloadAppliesRateAndIdentityChangesWithoutRestart(t *testing.T) {
	cfg, _ := reloadBase(t)
	r, _ := startRunner(t, cfg)

	updated := cfg
	updated.Endpoints = append([]config.Endpoint(nil), cfg.Endpoints...)
	updated.Endpoints[0].Frequency = 20
	updated.Endpoints[1].Identity = &config.IdentityConfig{Mode: "round-robin", Groups: []config.IdentityGroup{{Group: "mobile-app"}}}
	r.reload(updated)

	rates := r.rates()
	if rates["catalog"] != 20 {
		t.Fatalf("catalog rate = %v after reload, want 20", rates["catalog"])
	}
	if got := r.targets["orders"].current.Load().selector; got == nil {
		t.Fatal("orders lost its identity selector after reload")
	} else if _, ok := got.(*roundRobin); !ok {
		t.Fatalf("orders selector = %T, want round-robin after reload", got)
	}
}

func TestReloadAddsAndRemovesEndpoints(t *testing.T) {
	cfg, _ := reloadBase(t)
	r, out := startRunner(t, cfg)

	updated := cfg
	updated.Endpoints = []config.Endpoint{
		cfg.Endpoints[0],
		{Name: "profile", Path: "/profile", Methods: []string{"GET"}, Frequency: 2, Auth: "none"},
	}
	r.reload(updated)

	rates := r.rates()
	if _, removed := rates["orders"]; removed {
		t.Fatal("orders should stop after it is removed from the configuration")
	}
	if rates["profile"] != 2 {
		t.Fatalf("profile rate = %v, want 2 after it was added", rates["profile"])
	}
	if !strings.Contains(out.String(), "1 endpoint(s) updated, 1 added, 1 removed") {
		t.Fatalf("reload summary missing, got: %s", out.String())
	}
}

func TestRejectedEndpointChangeKeepsPreviousSettings(t *testing.T) {
	cfg, _ := reloadBase(t)
	r, out := startRunner(t, cfg)

	broken := cfg
	broken.Endpoints = append([]config.Endpoint(nil), cfg.Endpoints...)
	broken.Endpoints[1].Identity = &config.IdentityConfig{Mode: "weighted-groups", Groups: []config.IdentityGroup{{Group: "no-such-group", Weight: 100}}}
	r.reload(broken)

	if got := r.targets["orders"].current.Load().selector; got == nil {
		t.Fatal("rejected change removed the previous identity")
	}
	if !strings.Contains(out.String(), "change rejected, keeping the previous settings") {
		t.Fatalf("expected a rejection notice, got: %s", out.String())
	}
}

func TestStartupOnlySettingsProduceRestartNotice(t *testing.T) {
	cfg, _ := reloadBase(t)
	r, out := startRunner(t, cfg)

	updated := cfg
	updated.Load.MaxConcurrency = 50
	updated.Gateway.ProxyURL = "http://elsewhere:8000"
	r.reload(updated)

	for _, want := range []string{"gateway.proxy_url changed; restart run to apply it", "load.max_concurrency changed; restart run to apply it"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing notice %q, got: %s", want, out.String())
		}
	}
}
