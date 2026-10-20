package loadgen

import (
	"bytes"
	"context"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/kong/kong-loadgen/src/config"
	"github.com/kong/kong-loadgen/src/mock"
)

// runConfig points a single endpoint at a mock upstream started by the test.
func runConfig(t *testing.T, endpoint config.Endpoint, concurrency int) config.Config {
	t.Helper()
	server := httptest.NewServer(mock.NewHandler(config.Config{
		Endpoints: []config.Endpoint{endpoint},
	}))
	t.Cleanup(server.Close)
	return config.Config{
		Gateway:  config.GatewayConfig{ProxyURL: server.URL},
		Upstream: config.UpstreamConfig{DefaultURL: "http://localhost:9000"},
		Load: config.LoadConfig{
			MaxConcurrency: concurrency,
			Jitter:         0.2,
			Client:         config.ClientConfig{Timeout: config.Duration(5 * time.Second)},
		},
		Users:     config.UsersConfig{KeyHeader: "apikey"},
		Endpoints: []config.Endpoint{endpoint},
	}
}

func TestRunHitsTargetRateWithoutDrops(t *testing.T) {
	cfg := runConfig(t, config.Endpoint{
		Name: "catalog", Path: "/catalog", Methods: []string{"GET"}, Frequency: 40, Auth: "none",
	}, 50)

	summary, err := Run(context.Background(), cfg, Options{Duration: 2 * time.Second, Seed: 11})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.Dropped != 0 {
		t.Fatalf("dropped %d requests with ample concurrency", summary.Totals.Dropped)
	}
	// No ramp_up is set, so 40 rps for 2s should send about 80 requests.
	if got := summary.Totals.Requests; got < 72 || got > 88 {
		t.Fatalf("requests = %d, want about 80 (40 rps for 2s)", got)
	}
}

func TestOpenLoopDropsInsteadOfQueueingWhenConcurrencyIsFull(t *testing.T) {
	cfg := runConfig(t, config.Endpoint{
		Name: "slow", Path: "/slow", Methods: []string{"GET"}, Frequency: 50, Auth: "none",
		LatencyMS: config.LatencyConfig{Min: 300, Max: 300},
	}, 1)

	summary, err := Run(context.Background(), cfg, Options{Duration: 2 * time.Second, Seed: 12})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Totals.Dropped == 0 {
		t.Fatal("expected drops with one concurrency slot and a 300ms backend")
	}
	// With one slot and 300ms per request, only a handful can complete in 2s.
	if summary.Totals.Requests > 10 {
		t.Fatalf("completed %d requests, want at most about 7 with one slot", summary.Totals.Requests)
	}
}

func TestLiveViewRedrawsEverySecond(t *testing.T) {
	cfg := runConfig(t, config.Endpoint{
		Name: "catalog", Path: "/catalog", Methods: []string{"GET"}, Frequency: 10, Auth: "none",
	}, 20)

	var buffer lockedBuffer
	_, err := Run(context.Background(), cfg, Options{Duration: 2500 * time.Millisecond, Seed: 13, Live: true, Out: &buffer})
	if err != nil {
		t.Fatal(err)
	}
	output := buffer.String()
	if redraws := strings.Count(output, clearScreen); redraws < 2 {
		t.Fatalf("live view redrew %d times in 2.5s, want at least 2", redraws)
	}
	for _, heading := range []string{"ENDPOINT", "BUCKET", "Anonymous"} {
		if !strings.Contains(output, heading) {
			t.Fatalf("live view is missing %q", heading)
		}
	}
}

// lockedBuffer lets the live view and the test share a buffer safely.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
