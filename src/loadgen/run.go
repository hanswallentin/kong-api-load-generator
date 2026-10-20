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
	"sync/atomic"
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
	Color           bool          // colour the console; off when not a terminal
	Out             io.Writer

	// Reload signals that the configuration may have changed. LoadConfig reads
	// and validates it; on error the previous settings stay in effect.
	Reload     <-chan struct{}
	LoadConfig func() (config.Config, error)
	// DriftCheck returns notes about changes that configure has not pushed yet.
	DriftCheck func(config.Config) []string
}

// settings are the parts of an endpoint that can change while a run is going.
// The scheduler reads them through an atomic pointer, so a reload takes effect
// on the next request without restarting anything.
type settings struct {
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

// target is one endpoint being driven.
type target struct {
	name    string
	current atomic.Pointer[settings]
	stop    context.CancelFunc
}

// schedulerTick is how often each endpoint checks whether a request is due.
const schedulerTick = 5 * time.Millisecond

// maxNotices bounds the messages kept for the live view.
const maxNotices = 5

type runner struct {
	cfg      config.Config // startup configuration: proxy, timing and concurrency
	opts     Options
	src      *source
	client   *http.Client
	stats    *collector
	sem      chan struct{}
	start    time.Time
	ctx      context.Context
	inFlight sync.WaitGroup
	// schedulers counts running scheduler goroutines.
	schedulers sync.WaitGroup

	mu      sync.Mutex
	targets map[string]*target
	order   []string // endpoint names in configuration order
	notices []notice
}

// notice is a message for the operator: a warning, or information such as a reload.
type notice struct {
	at      time.Time
	text    string
	warning bool
}

// Run sends traffic until the duration elapses or ctx is cancelled, then
// returns the statistics. In-flight requests are allowed to finish.
func Run(ctx context.Context, cfg config.Config, opts Options) (Summary, error) {
	if opts.Out == nil {
		opts.Out = io.Discard
	}
	src := newSource(opts.Seed)
	concurrency := cfg.Load.MaxConcurrency
	r := &runner{
		cfg:     cfg,
		opts:    opts,
		src:     src,
		client:  newClient(cfg.Load.Client.Timeout.Value(), concurrency),
		stats:   newCollector(src, opts.VerboseIdentity),
		sem:     make(chan struct{}, concurrency),
		targets: map[string]*target{},
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
	r.ctx = ctx
	r.start = time.Now()

	for _, endpoint := range cfg.Endpoints {
		s, warnings, err := newSettings(cfg, endpoint, src)
		if err != nil {
			return Summary{}, err
		}
		for _, warning := range warnings {
			r.notice(warning)
		}
		t := &target{name: endpoint.Name}
		t.current.Store(s)
		r.targets[endpoint.Name] = t
		r.order = append(r.order, endpoint.Name)
	}
	r.checkEnvironment(cfg)
	r.lockedStartAll()

	liveDone := make(chan struct{})
	if opts.Live {
		go func() {
			defer close(liveDone)
			r.liveView()
		}()
	} else {
		close(liveDone)
	}
	reloadDone := make(chan struct{})
	if opts.Reload != nil && opts.LoadConfig != nil {
		go func() {
			defer close(reloadDone)
			r.reloadLoop()
		}()
	} else {
		close(reloadDone)
	}

	<-ctx.Done()
	<-reloadDone
	<-liveDone
	r.schedulers.Wait()
	r.waitInFlight(cfg.Load.Client.Timeout.Value() + time.Second)
	return r.stats.snapshot(r.rates()), nil
}

// checkEnvironment reports problems that do not stop the run.
func (r *runner) checkEnvironment(cfg config.Config) {
	if mock.IsBuiltin(cfg.Upstream.DefaultURL) && !reachable(cfg.Upstream.DefaultURL) {
		r.notice(fmt.Sprintf("upstream %s is not reachable; start the built-in mock with: kong-loadgen upstream", cfg.Upstream.DefaultURL))
	}
	if r.opts.DriftCheck != nil {
		for _, note := range r.opts.DriftCheck(cfg) {
			r.notice(note)
		}
	}
}

// lockedStartAll starts a scheduler for every endpoint in the runner.
func (r *runner) lockedStartAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, name := range r.order {
		r.startLocked(r.targets[name])
	}
}

// startLocked starts one endpoint's scheduler. The caller must hold r.mu.
func (r *runner) startLocked(t *target) {
	ctx, cancel := context.WithCancel(r.ctx)
	t.stop = cancel
	r.schedulers.Add(1)
	go func() {
		defer r.schedulers.Done()
		r.schedule(ctx, t)
	}()
}

// reloadLoop applies a new configuration each time it is signalled.
func (r *runner) reloadLoop() {
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-r.opts.Reload:
			cfg, err := r.opts.LoadConfig()
			if err != nil {
				r.notice(fmt.Sprintf("config reload rejected, keeping the previous settings: %v", err))
				continue
			}
			r.reload(cfg)
		}
	}
}

// reload applies endpoint, identity and rate changes to the running endpoints,
// starts newly added endpoints and stops removed ones. Settings that cannot
// change safely mid-run produce a restart notice instead.
func (r *runner) reload(cfg config.Config) {
	for _, note := range restartNotes(r.cfg, cfg) {
		r.notice(note)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ctx.Err() != nil {
		return
	}
	updated, added, removed := 0, 0, 0
	seen := map[string]bool{}
	var order []string
	for _, endpoint := range cfg.Endpoints {
		seen[endpoint.Name] = true
		order = append(order, endpoint.Name)
		s, warnings, err := newSettings(cfg, endpoint, r.src)
		if err != nil {
			r.noticeLocked(notice{at: time.Now(), text: fmt.Sprintf("endpoint %q change rejected, keeping the previous settings: %v", endpoint.Name, err), warning: true})
			continue
		}
		for _, warning := range warnings {
			r.noticeLocked(notice{at: time.Now(), text: warning, warning: true})
		}
		if existing, ok := r.targets[endpoint.Name]; ok {
			existing.current.Store(s)
			updated++
			continue
		}
		t := &target{name: endpoint.Name}
		t.current.Store(s)
		r.targets[endpoint.Name] = t
		r.startLocked(t)
		added++
	}
	for name, t := range r.targets {
		if !seen[name] {
			t.stop()
			delete(r.targets, name)
			removed++
		}
	}
	r.order = order
	r.noticeLocked(notice{at: time.Now(), text: fmt.Sprintf("config reloaded: %d endpoint(s) updated, %d added, %d removed", updated, added, removed)})

	if r.opts.DriftCheck != nil {
		for _, note := range r.opts.DriftCheck(cfg) {
			r.noticeLocked(notice{at: time.Now(), text: note, warning: true})
		}
	}
}

// restartNotes names the startup-only settings that changed.
func restartNotes(old, updated config.Config) []string {
	var notes []string
	restart := func(changed bool, name string) {
		if changed {
			notes = append(notes, fmt.Sprintf("%s changed; restart run to apply it", name))
		}
	}
	restart(old.Gateway.ProxyURL != updated.Gateway.ProxyURL, "gateway.proxy_url")
	restart(old.Load.MaxConcurrency != updated.Load.MaxConcurrency, "load.max_concurrency")
	restart(old.Load.RampUp != updated.Load.RampUp, "load.ramp_up")
	restart(old.Load.Jitter != updated.Load.Jitter, "load.jitter")
	restart(old.Load.Duration != updated.Load.Duration, "load.duration")
	restart(old.Load.Client.Timeout != updated.Load.Client.Timeout, "load.client.timeout")
	return notes
}

// notice records a warning.
func (r *runner) notice(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.noticeLocked(notice{at: time.Now(), text: message, warning: true})
}

// noticeLocked shows a message in the live view while it runs, or on output otherwise.
// The caller must hold r.mu.
func (r *runner) noticeLocked(n notice) {
	if r.opts.Live {
		r.notices = append(r.notices, n)
		if len(r.notices) > maxNotices {
			r.notices = r.notices[len(r.notices)-maxNotices:]
		}
		return
	}
	fmt.Fprintln(r.opts.Out, formatNotice(r.opts.Color, n))
}

// rates returns the configured requests per second for each running endpoint.
func (r *runner) rates() map[string]float64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	rates := make(map[string]float64, len(r.targets))
	for name, t := range r.targets {
		rates[name] = t.current.Load().rps
	}
	return rates
}

// recentNotices returns the notices to show in the live view.
func (r *runner) recentNotices() []notice {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notice(nil), r.notices...)
}

// endpointOrder returns the current endpoint names in configuration order.
func (r *runner) endpointOrder() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.order...)
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

// schedule fires one endpoint's requests in an open loop. Each tick adds the
// requests the rate allows, so ramp-up and jitter change the timing without
// drifting the average rate. Requests are never delayed by slow responses.
func (r *runner) schedule(ctx context.Context, t *target) {
	ticker := time.NewTicker(schedulerTick)
	defer ticker.Stop()
	// Start at a random point in the first interval so endpoints do not align.
	credit := r.src.float64()
	need := r.nextNeed()
	last := r.start
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s := t.current.Load()
			credit += s.rps * r.rampFactor(now) * now.Sub(last).Seconds()
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
func (r *runner) rampFactor(now time.Time) float64 {
	ramp := r.cfg.Load.RampUp.Value()
	if ramp <= 0 {
		return 1
	}
	return math.Min(1, now.Sub(r.start).Seconds()/ramp.Seconds())
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
	r.perform(t, chooseError(r.src, t.current.Load().errors))
}

// perform builds one request that should produce kind, performs it, and records its outcome.
func (r *runner) perform(t *target, kind errorKind) {
	s := t.current.Load()
	identity := anonymous()
	if kind == noError || kind == upstreamError || kind == malformedBody {
		if s.selector != nil {
			identity = s.selector.pick(r.src)
		}
	}
	if kind == wrongKey {
		identity = Identity{Bucket: BucketInvalid, Key: fmt.Sprintf("wrong-key-%06d", r.src.intN(1000000))}
	}

	path := s.path
	if kind == unknownPath {
		path = fmt.Sprintf("/loadgen-missing-%06d", r.src.intN(1000000))
	}
	method := s.methods[r.src.intN(len(s.methods))]
	body := s.body
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
	for name, value := range s.headers {
		req.Header.Set(name, value)
	}
	if identity.Key != "" {
		req.Header.Set(s.keyHeader, identity.Key)
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if s.latencyMax > 0 {
		req.Header.Set(mock.LatencyMinHeader, strconv.Itoa(s.latencyMin))
		req.Header.Set(mock.LatencyMaxHeader, strconv.Itoa(s.latencyMax))
	}
	if kind == upstreamError {
		req.Header.Set(mock.ErrorHeader, strconv.Itoa(s.statuses[r.src.intN(len(s.statuses))]))
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

// newSettings resolves one endpoint's configuration. It returns warnings that
// do not stop the endpoint from running.
func newSettings(cfg config.Config, endpoint config.Endpoint, src *source) (*settings, []string, error) {
	upstream := cfg.Upstream.DefaultURL
	if endpoint.UpstreamURL != nil {
		upstream = *endpoint.UpstreamURL
	}
	builtin := mock.IsBuiltin(upstream)
	mode := endpoint.EffectiveErrorMode()
	var warnings []string
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
	return &settings{
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
	}, warnings, nil
}

// buildTargets resolves every endpoint into a target. Tests use it to drive
// perform directly; Run builds targets itself.
func buildTargets(cfg config.Config, src *source) ([]*target, error) {
	var targets []*target
	for _, endpoint := range cfg.Endpoints {
		s, _, err := newSettings(cfg, endpoint, src)
		if err != nil {
			return nil, err
		}
		t := &target{name: endpoint.Name}
		t.current.Store(s)
		targets = append(targets, t)
	}
	return targets, nil
}

// reachable reports whether something accepts connections at the URL's host and port.
func reachable(rawURL string) bool {
	listen, err := mock.ListenAddress(rawURL)
	if err != nil {
		return false
	}
	conn, err := net.DialTimeout("tcp", "127.0.0.1"+listen, time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
