package loadgen

import (
	"sort"
	"sync"
	"time"
)

// latencySampleSize bounds memory per endpoint. Reservoir sampling keeps the
// percentiles representative however long the run lasts.
const latencySampleSize = 20000

type counter struct {
	Requests int64
	Errors   int64
}

type endpointStats struct {
	Requests  int64
	Errors    int64
	Transport int64
	Dropped   int64
	Status2xx int64
	Status3xx int64
	Status4xx int64
	Status5xx int64
	Status429 int64
	seen      int64
	latencies []float64
}

// collector is safe for concurrent use. Each request is recorded once, in one
// endpoint and one attribution bucket.
type collector struct {
	mu          sync.Mutex
	src         *source
	started     time.Time
	endpoints   map[string]*endpointStats
	endpointIDs []string
	buckets     map[string]*counter
	bucketIDs   []string
	users       map[string]*counter
	verbose     bool
}

func newCollector(src *source, verbose bool) *collector {
	return &collector{
		src:       src,
		started:   time.Now(),
		endpoints: map[string]*endpointStats{},
		buckets:   map[string]*counter{},
		users:     map[string]*counter{},
		verbose:   verbose,
	}
}

func (c *collector) endpoint(name string) *endpointStats {
	stats, ok := c.endpoints[name]
	if !ok {
		stats = &endpointStats{}
		c.endpoints[name] = stats
		c.endpointIDs = append(c.endpointIDs, name)
	}
	return stats
}

func (c *collector) bucket(name string) *counter {
	bucket, ok := c.buckets[name]
	if !ok {
		bucket = &counter{}
		c.buckets[name] = bucket
		c.bucketIDs = append(c.bucketIDs, name)
	}
	return bucket
}

// recordDropped counts a scheduled request skipped because max_concurrency was full.
func (c *collector) recordDropped(endpoint string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.endpoint(endpoint).Dropped++
}

// recordResponse counts a completed request. status is 0 for transport errors.
func (c *collector) recordResponse(endpoint string, identity Identity, status int, latencyMS float64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stats := c.endpoint(endpoint)
	stats.Requests++

	failed := status == 0 || status >= 400
	if status == 0 {
		stats.Transport++
	} else {
		stats.addLatency(c.src, latencyMS)
		switch {
		case status == 429:
			stats.Status429++
			stats.Status4xx++
		case status >= 500:
			stats.Status5xx++
		case status >= 400:
			stats.Status4xx++
		case status >= 300:
			stats.Status3xx++
		default:
			stats.Status2xx++
		}
	}
	if failed {
		stats.Errors++
	}

	bucket := c.bucket(identity.Bucket)
	bucket.Requests++
	if failed {
		bucket.Errors++
	}
	if c.verbose && identity.Username != "" {
		user, ok := c.users[identity.Username]
		if !ok {
			user = &counter{}
			c.users[identity.Username] = user
		}
		user.Requests++
		if failed {
			user.Errors++
		}
	}
}

// addLatency adds a sample with reservoir sampling (Algorithm R).
func (s *endpointStats) addLatency(src *source, latencyMS float64) {
	s.seen++
	if len(s.latencies) < latencySampleSize {
		s.latencies = append(s.latencies, latencyMS)
		return
	}
	if index := src.intN(int(s.seen)); index < latencySampleSize {
		s.latencies[index] = latencyMS
	}
}

func percentile(samples []float64, p float64) float64 {
	if len(samples) == 0 {
		return 0
	}
	sorted := append([]float64(nil), samples...)
	sort.Float64s(sorted)
	index := int(p/100*float64(len(sorted))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}

// requestCounts returns the cumulative request count per endpoint, used by the
// live view to compute the actual request rate between ticks.
func (c *collector) requestCounts() map[string]int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	counts := make(map[string]int64, len(c.endpoints))
	for name, stats := range c.endpoints {
		counts[name] = stats.Requests
	}
	return counts
}
