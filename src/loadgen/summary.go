package loadgen

import (
	"sort"
	"time"
)

// Summary is the statistics snapshot shown at exit and written by --json-summary.
type Summary struct {
	StartedAt       time.Time      `json:"started_at"`
	DurationSeconds float64        `json:"duration_seconds"`
	Totals          Totals         `json:"totals"`
	Endpoints       []EndpointStat `json:"endpoints"`
	Buckets         []BucketStat   `json:"buckets"`
	Users           []BucketStat   `json:"users,omitempty"`
}

type Totals struct {
	Requests  int64   `json:"requests"`
	Errors    int64   `json:"errors"`
	Dropped   int64   `json:"dropped"`
	ActualRPS float64 `json:"actual_rps"`
	ErrorPct  float64 `json:"error_pct"`
}

type EndpointStat struct {
	Name      string  `json:"name"`
	TargetRPS float64 `json:"target_rps"`
	Requests  int64   `json:"requests"`
	ActualRPS float64 `json:"actual_rps"`
	Errors    int64   `json:"errors"`
	ErrorPct  float64 `json:"error_pct"`
	Dropped   int64   `json:"dropped"`
	Transport int64   `json:"transport_errors"`
	Status2xx int64   `json:"status_2xx"`
	Status3xx int64   `json:"status_3xx"`
	Status4xx int64   `json:"status_4xx"`
	Status5xx int64   `json:"status_5xx"`
	Status429 int64   `json:"status_429"`
	P50Ms     float64 `json:"p50_ms"`
	P95Ms     float64 `json:"p95_ms"`
	P99Ms     float64 `json:"p99_ms"`
}

// BucketStat is used for both attribution buckets and individual users.
type BucketStat struct {
	Name     string  `json:"name"`
	Requests int64   `json:"requests"`
	Errors   int64   `json:"errors"`
	SharePct float64 `json:"share_pct"`
	ErrorPct float64 `json:"error_pct"`
}

// snapshot builds a Summary from the collector. targets maps endpoint name to
// its configured request rate.
func (c *collector) snapshot(targets map[string]float64) Summary {
	c.mu.Lock()
	defer c.mu.Unlock()
	elapsed := time.Since(c.started).Seconds()
	summary := Summary{StartedAt: c.started, DurationSeconds: elapsed}

	for _, name := range c.endpointIDs {
		stats := c.endpoints[name]
		summary.Endpoints = append(summary.Endpoints, EndpointStat{
			Name:      name,
			TargetRPS: targets[name],
			Requests:  stats.Requests,
			ActualRPS: rate(stats.Requests, elapsed),
			Errors:    stats.Errors,
			ErrorPct:  pct(stats.Errors, stats.Requests),
			Dropped:   stats.Dropped,
			Transport: stats.Transport,
			Status2xx: stats.Status2xx,
			Status3xx: stats.Status3xx,
			Status4xx: stats.Status4xx,
			Status5xx: stats.Status5xx,
			Status429: stats.Status429,
			P50Ms:     percentile(stats.latencies, 50),
			P95Ms:     percentile(stats.latencies, 95),
			P99Ms:     percentile(stats.latencies, 99),
		})
		summary.Totals.Requests += stats.Requests
		summary.Totals.Errors += stats.Errors
		summary.Totals.Dropped += stats.Dropped
	}
	summary.Totals.ActualRPS = rate(summary.Totals.Requests, elapsed)
	summary.Totals.ErrorPct = pct(summary.Totals.Errors, summary.Totals.Requests)

	summary.Buckets = bucketStats(c.bucketIDs, c.buckets, summary.Totals.Requests)
	if c.verbose {
		names := make([]string, 0, len(c.users))
		for name := range c.users {
			names = append(names, name)
		}
		sort.Strings(names)
		summary.Users = bucketStats(names, c.users, summary.Totals.Requests)
	}
	return summary
}

func bucketStats(names []string, counters map[string]*counter, total int64) []BucketStat {
	stats := make([]BucketStat, 0, len(names))
	for _, name := range names {
		bucket := counters[name]
		stats = append(stats, BucketStat{
			Name:     name,
			Requests: bucket.Requests,
			Errors:   bucket.Errors,
			SharePct: pct(bucket.Requests, total),
			ErrorPct: pct(bucket.Errors, bucket.Requests),
		})
	}
	return stats
}

func rate(count int64, seconds float64) float64 {
	if seconds <= 0 {
		return 0
	}
	return float64(count) / seconds
}

func pct(part, whole int64) float64 {
	if whole == 0 {
		return 0
	}
	return float64(part) * 100 / float64(whole)
}
