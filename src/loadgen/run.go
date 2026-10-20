// Package loadgen sends the configured traffic through the Kong proxy and
// records how each request is attributed and how it ended.
package loadgen

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kong/kong-loadgen/src/config"
	"github.com/kong/kong-loadgen/src/mock"
)

// Options controls one load run.
type Options struct {
	Duration        time.Duration // overrides load.duration when non-zero
	Seed            uint64        // 0 picks a random seed
	VerboseIdentity bool          // break statistics down per user
	Live            bool          // redraw the live console every second
	Out             io.Writer
}

// target is one endpoint with its load settings resolved from the config.
type target struct {
	name       string
	path       string
	rps        float64
	methods    []string
	headers    map[string]string
	body       []byte
	keyHeader  string
	latencyMin int
	latencyMax int
	errors     errorPlan
	statuses   []int
	selector   selector
}

type runner struct {
	cfg      config.Config
	src      *source
	client   *http.Client
	stats    *collector
	targets  []*target
	sem      chan struct{}
	inFlight sync.WaitGroup
}

// Run sends traffic until the duration elapses or ctx is cancelled, then
// returns the statistics. In-flight requests are allowed to finish.
func Run(ctx context.Context, cfg config.Config, opts Options) (Summary, error) {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	src := newSource(opts.Seed)
	targets, warnings, err := buildTargets(cfg)
	if err != nil {
		return Summary{}, err
	}
	for _, warning := range warnings {
		fmt.Fprintf(opts.Out, "warning: %s\n", warning)
	}
	if mock.IsBuiltin(cfg.Upstream.DefaultURL) && !reachable(cfg.Upstream.DefaultURL) {
		fmt.Fprintf(opts.Out, "warning: upstream %s is not reachable; start the built-in mock with: kong-loadgen upstream\n", cfg.Upstream.DefaultURL)
	}

	duration := cfg.Load.Duration.Value()
	if opts.Duration > 0 {
		duration = opts.Duration
	}
	if duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, duration)
		defer cancel()
	}

	concurrency := cfg.Load.MaxConcurrency
	r := &runner{
		cfg:     cfg,
		src:     src,
		client:  newClient(cfg.Load.Client.Timeout.Value(), concurrency),
		stats:   newCollector(src, opts.VerboseIdentity),
		targets: targets,
		sem:     make(chan struct{}, concurrency),
	}
	rates := map[string]float64{}
	for _, t := range targets {
		rates[t.name] = t.rps
	}

	start := time.Now()
	liveDone := make(chan struct{})
	if opts.Live {
		go func() {
			defer close(liveDone)
			r.liveView(ctx, rates, start, opts.Out)
		}()
	} else {
		close(liveDone)
	}

	var schedulers sync.WaitGroup
	for _, t := range targets {
		schedulers.Add(1)
		go func(t *target) {
			defer schedulers.Done()
			r.schedule(ctx, t, start)
		}(t)
	}
	schedulers.Wait()
	<-liveDone
	r.waitInFlight(cfg.Load.Client.Timeout.Value() + time.Second)
	return r.stats.snapshot(rates), nil
}

func newClient(timeout time.Duration, maxConcurrency int) *http.Client {
	transport := &http.Transport{
		Proxy:               nil,
		MaxIdleConns:        maxConcurrency,
		MaxIdleConnsPerHost: maxConcurrency,
		IdleConnTimeout:     90 * time.Second,
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// waitInFlight waits for outstanding requests, up to limit.
func (r *runner) waitInFlight(limit time.Duration) {
	done := make(chan struct{})
	go func() {
		r.inFlight.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(limit):
	}
}

// schedulerTick is how often each endpoint checks whether a request is due.
const schedulerTick = 5 * time.Millisecond

// schedule fires one endpoint's requests in an open loop. Each tick adds the
// requests the rate allows, so ramp-up and jitter change the timing without
// drifting the average rate. Requests are never delayed by slow responses.
func (r *runner) schedule(ctx context.Context, t *target, start time.Time) {
	ticker := time.NewTicker(schedulerTick)
	defer ticker.Stop()
	// Start at a random point in the first interval so endpoints do not align.
	credit := r.src.float64()
	need := r.nextNeed()
	last := start
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			credit += t.rps * r.rampFactor(now, start) * now.Sub(last).Seconds()
			last = now
			for credit >= need {
				credit -= need
				need = r.nextNeed()
				r.fire(t)
			}
		}
	}
}

// rampFactor scales the rate from near zero up to 1 over load.ramp_up.
func (r *runner) rampFactor(now, start time.Time) float64 {
	ramp := r.cfg.Load.RampUp.Value()
	if ramp <= 0 {
		return 1
	}
	return math.Min(1, now.Sub(start).Seconds()/ramp.Seconds())
}

// nextNeed is how much credit the next request costs: one request, varied by
// load.jitter so the traffic does not look mechanical.
func (r *runner) nextNeed() float64 {
	jitter := r.cfg.Load.Jitter
	if jitter <= 0 {
		return 1
	}
	return 1 + jitter*(2*r.src.float64()-1)
}

// fire starts one request if a concurrency slot is free; otherwise the request is dropped.
func (r *runner) fire(t *target) {
	select {
	case r.sem <- struct{}{}:
		r.inFlight.Add(1)
		go func() {
			defer func() {
				<-r.sem
				r.inFlight.Done()
			}()
			r.send(t)
		}()
	default:
		r.stats.recordDropped(t.name)
	}
}

// send chooses this request's error kind, then builds and performs the request.
func (r *runner) send(t *target) {
	r.perform(t, chooseError(r.src, t.errors))
}

// perform builds one request that should produce kind, performs it, and records its outcome.
func (r *runner) perform(t *target, kind errorKind) {
	identity := anonymous()
	if kind == noError || kind == upstreamError || kind == malformedBody {
		if t.selector != nil {
			identity = t.selector.pick(r.src)
		}
	}
	if kind == wrongKey {
		identity = Identity{Bucket: BucketInvalid, Key: fmt.Sprintf("wrong-key-%06d", r.src.intN(1000000))}
	}

	path := t.path
	if kind == unknownPath {
		path = fmt.Sprintf("/loadgen-missing-%06d", r.src.intN(1000000))
	}
	method := t.methods[r.src.intN(len(t.methods))]
	body := t.body
	if kind == malformedBody {
		body = []byte(`{"loadgen": malformed`)
	}

	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, strings.TrimRight(r.cfg.Gateway.ProxyURL, "/")+path, reader)
	if err != nil {
		r.stats.recordResponse(t.name, identity, 0, 0)
		return
	}
	for name, value := range t.headers {
		req.Header.Set(name, value)
	}
	if identity.Key != "" {
		req.Header.Set(t.keyHeader, identity.Key)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if t.latencyMax > 0 {
		req.Header.Set(mock.LatencyMinHeader, strconv.Itoa(t.latencyMin))
		req.Header.Set(mock.LatencyMaxHeader, strconv.Itoa(t.latencyMax))
	}
	if kind == upstreamError {
		req.Header.Set(mock.ErrorHeader, strconv.Itoa(t.statuses[r.src.intN(len(t.statuses))]))
	}

	started := time.Now()
	resp, err := r.client.Do(req)
	status := 0
	if err == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		status = resp.StatusCode
	}
	latency := float64(time.Since(started)) / float64(time.Millisecond)
	r.stats.recordResponse(t.name, identity, status, latency)
}

// buildTargets resolves every endpoint into a target and collects warnings that
// do not stop the run.
func buildTargets(cfg config.Config) ([]*target, []string, error) {
	var targets []*target
	var warnings []string
	for _, endpoint := range cfg.Endpoints {
		upstream := cfg.Upstream.DefaultURL
		if endpoint.UpstreamURL != nil {
			upstream = *endpoint.UpstreamURL
		}
		builtin := mock.IsBuiltin(upstream)
		mode := endpoint.EffectiveErrorMode()
		if mode == "upstream" && !builtin {
			warnings = append(warnings, fmt.Sprintf("endpoint %q uses error_mode: upstream, but its upstream %s is not the built-in mock; using client errors instead", endpoint.Name, upstream))
		}
		selector, err := newSelector(endpoint.Identity, cfg.Users.Groups)
		if err != nil {
			return nil, nil, fmt.Errorf("endpoint %q: %w", endpoint.Name, err)
		}
		methods := endpoint.Methods
		if len(methods) == 0 {
			methods = []string{"GET"}
		}
		headers := map[string]string{}
		for name, value := range endpoint.Headers {
			headers[name] = value
		}
		var body []byte
		if endpoint.Body != "" {
			body = []byte(endpoint.Body)
		}
		targets = append(targets, &target{
			name:       endpoint.Name,
			path:       endpoint.Path,
			rps:        endpoint.RequestsPerSecond(),
			methods:    methods,
			headers:    headers,
			body:       body,
			keyHeader:  cfg.EffectiveKeyHeader(),
			latencyMin: endpoint.LatencyMS.Min,
			latencyMax: endpoint.LatencyMS.Max,
			errors: errorPlan{
				percentage: endpoint.ErrorPercentage,
				mode:       mode,
				builtin:    builtin,
				keyAuth:    endpoint.Auth == "key-auth",
			},
			statuses: cfg.Load.EffectiveErrorStatuses(),
			selector: selector,
		})
	}
	return targets, warnings, nil
}

// reachable reports whether something accepts connections at the URL's host and port.
func reachable(rawURL string) bool {
	address, err := mockAddress(rawURL)
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func mockAddress(rawURL string) (string, error) {
	listen, err := mock.ListenAddress(rawURL)
	if err != nil {
		return "", err
	}
	return "127.0.0.1" + listen, nil
}
