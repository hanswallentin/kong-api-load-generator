package loadgen

import (
	"context"
	"fmt"
	"io"
	"text/tabwriter"
	"time"
)

const clearScreen = "\x1b[H\x1b[2J"

// liveView redraws the console every second until ctx is cancelled.
func (r *runner) liveView(ctx context.Context, rates map[string]float64, start time.Time, out io.Writer) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	previous := map[string]int64{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		counts := r.stats.requestCounts()
		window := make(map[string]float64, len(counts))
		for name, count := range counts {
			window[name] = float64(count - previous[name])
		}
		previous = counts
		summary := r.stats.snapshot(rates)
		fmt.Fprint(out, clearScreen)
		renderLive(out, summary, window, time.Since(start))
	}
}

// renderLive draws the live view. window holds requests per endpoint in the last second.
func renderLive(out io.Writer, summary Summary, window map[string]float64, elapsed time.Duration) {
	fmt.Fprintf(out, "kong-loadgen  elapsed %s  requests %d  errors %.1f%%  dropped %d\n\n",
		formatElapsed(elapsed), summary.Totals.Requests, summary.Totals.ErrorPct, summary.Totals.Dropped)
	writeEndpointTable(out, summary.Endpoints, window)
	fmt.Fprintln(out)
	writeBucketTable(out, "BUCKET", summary.Buckets)
	if len(summary.Users) > 0 {
		fmt.Fprintln(out)
		writeBucketTable(out, "USER", summary.Users)
	}
}

// PrintSummary writes the statistics shown when a run ends.
func PrintSummary(out io.Writer, summary Summary) {
	fmt.Fprintf(out, "\nRun summary (%s)\n", formatElapsed(time.Duration(summary.DurationSeconds*float64(time.Second))))
	fmt.Fprintf(out, "Requests %d  actual %.1f rps  errors %.1f%%  dropped %d\n\n",
		summary.Totals.Requests, summary.Totals.ActualRPS, summary.Totals.ErrorPct, summary.Totals.Dropped)
	writeEndpointTable(out, summary.Endpoints, nil)
	fmt.Fprintln(out)
	writeBucketTable(out, "BUCKET", summary.Buckets)
	if len(summary.Users) > 0 {
		fmt.Fprintln(out)
		writeBucketTable(out, "USER", summary.Users)
	}
}

// writeEndpointTable prints one row per endpoint. A nil window uses the run average rate.
func writeEndpointTable(out io.Writer, endpoints []EndpointStat, window map[string]float64) {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintln(table, "ENDPOINT\tTARGET\tACTUAL\tSUCCESS\tERROR\t2XX\t4XX\t5XX\t429\tP50\tP95\tP99")
	for _, e := range endpoints {
		actual := e.ActualRPS
		if window != nil {
			actual = window[e.Name]
		}
		fmt.Fprintf(table, "%s\t%.1f\t%.1f\t%.1f%%\t%.1f%%\t%d\t%d\t%d\t%d\t%.0fms\t%.0fms\t%.0fms\n",
			e.Name, e.TargetRPS, actual, 100-e.ErrorPct, e.ErrorPct,
			e.Status2xx, e.Status4xx, e.Status5xx, e.Status429, e.P50Ms, e.P95Ms, e.P99Ms)
	}
	_ = table.Flush()
}

func writeBucketTable(out io.Writer, heading string, buckets []BucketStat) {
	table := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	fmt.Fprintf(table, "%s\tREQUESTS\tSHARE\tERRORS\n", heading)
	for _, b := range buckets {
		fmt.Fprintf(table, "%s\t%d\t%.1f%%\t%.1f%%\n", b.Name, b.Requests, b.SharePct, b.ErrorPct)
	}
	_ = table.Flush()
}

func formatElapsed(d time.Duration) string {
	total := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, (total%3600)/60, total%60)
}
