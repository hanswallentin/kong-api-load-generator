package loadgen

import (
	"fmt"
	"io"
	"strings"
	"time"
)

const clearScreen = "\x1b[H\x1b[2J"

// ANSI styles. They are only written when colour is on.
const (
	styleBold   = "1"
	styleRed    = "31"
	styleYellow = "33"
)

func paint(on bool, style, text string) string {
	if !on || style == "" {
		return text
	}
	return "\x1b[" + style + "m" + text + "\x1b[0m"
}

// formatNotice writes a timestamped line, with warnings in yellow when colour is on.
func formatNotice(color bool, n notice) string {
	stamp := n.at.Format("15:04:05")
	if !n.warning {
		return fmt.Sprintf("%s %s", stamp, n.text)
	}
	return fmt.Sprintf("%s %s", stamp, paint(color, styleYellow, "warning: "+n.text))
}

// liveView redraws the console every second until the run ends.
func (r *runner) liveView() {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	previous := map[string]int64{}
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
		}
		counts := r.stats.requestCounts()
		window := make(map[string]float64, len(counts))
		for name, count := range counts {
			window[name] = float64(count - previous[name])
		}
		previous = counts
		summary := r.stats.snapshot(r.rates())
		fmt.Fprint(r.opts.Out, clearScreen)
		renderLive(r.opts.Out, r.opts.Color, summary, window, time.Since(r.start), r.endpointOrder(), r.recentNotices())
	}
}

// renderLive draws the live view. window holds requests per endpoint in the last second.
func renderLive(out io.Writer, color bool, summary Summary, window map[string]float64, elapsed time.Duration, order []string, notices []notice) {
	fmt.Fprintf(out, "%s  elapsed %s  requests %d  errors %.1f%%  dropped %d\n\n",
		paint(color, styleBold, "kong-loadgen"), formatElapsed(elapsed),
		summary.Totals.Requests, summary.Totals.ErrorPct, summary.Totals.Dropped)
	writeEndpointTable(out, color, summary.Endpoints, window, order)
	fmt.Fprintln(out)
	writeBucketTable(out, color, "BUCKET", summary.Buckets)
	if len(summary.Users) > 0 {
		fmt.Fprintln(out)
		writeBucketTable(out, color, "USER", summary.Users)
	}
	if len(notices) > 0 {
		fmt.Fprintln(out)
		for _, n := range notices {
			fmt.Fprintln(out, formatNotice(color, n))
		}
	}
}

// PrintSummary writes the statistics shown when a run ends.
func PrintSummary(out io.Writer, color bool, summary Summary) {
	fmt.Fprintf(out, "\n%s (%s)\n", paint(color, styleBold, "Run summary"),
		formatElapsed(time.Duration(summary.DurationSeconds*float64(time.Second))))
	fmt.Fprintf(out, "Requests %d  actual %.1f rps  errors %.1f%%  dropped %d\n\n",
		summary.Totals.Requests, summary.Totals.ActualRPS, summary.Totals.ErrorPct, summary.Totals.Dropped)
	order := make([]string, 0, len(summary.Endpoints))
	for _, endpoint := range summary.Endpoints {
		order = append(order, endpoint.Name)
	}
	writeEndpointTable(out, color, summary.Endpoints, nil, order)
	fmt.Fprintln(out)
	writeBucketTable(out, color, "BUCKET", summary.Buckets)
	if len(summary.Users) > 0 {
		fmt.Fprintln(out)
		writeBucketTable(out, color, "USER", summary.Users)
	}
}

// cell is one table value. Colour is applied after padding, so escape
// sequences never affect column widths.
type cell struct {
	text  string
	style string
}

func plain(text string) cell { return cell{text: text} }

// writeTable prints rows with each column padded to its widest value.
func writeTable(out io.Writer, color bool, header []string, rows [][]cell) {
	widths := make([]int, len(header))
	for i, title := range header {
		widths[i] = len(title)
	}
	for _, row := range rows {
		for i, c := range row {
			if len(c.text) > widths[i] {
				widths[i] = len(c.text)
			}
		}
	}
	write := func(cells []cell, headerRow bool) {
		var line strings.Builder
		for i, c := range cells {
			padded := c.text + strings.Repeat(" ", widths[i]-len(c.text))
			style := c.style
			if headerRow {
				style = styleBold
			}
			if i > 0 {
				line.WriteString("  ")
			}
			if i == len(cells)-1 {
				// Trailing padding is not needed on the last column.
				padded = c.text
			}
			line.WriteString(paint(color, style, padded))
		}
		fmt.Fprintln(out, line.String())
	}
	headerCells := make([]cell, len(header))
	for i, title := range header {
		headerCells[i] = plain(title)
	}
	write(headerCells, true)
	for _, row := range rows {
		write(row, false)
	}
}

// writeEndpointTable prints one row per endpoint, in configuration order. A
// nil window uses the run average rate.
func writeEndpointTable(out io.Writer, color bool, endpoints []EndpointStat, window map[string]float64, order []string) {
	byName := make(map[string]EndpointStat, len(endpoints))
	for _, e := range endpoints {
		byName[e.Name] = e
	}
	header := []string{"ENDPOINT", "TARGET", "ACTUAL", "SUCCESS", "ERROR", "2XX", "4XX", "5XX", "429", "P50", "P95", "P99"}
	var rows [][]cell
	for _, name := range order {
		e, ok := byName[name]
		if !ok {
			continue
		}
		actual := e.ActualRPS
		if window != nil {
			actual = window[e.Name]
		}
		rows = append(rows, []cell{
			plain(e.Name),
			plain(fmt.Sprintf("%.1f", e.TargetRPS)),
			plain(fmt.Sprintf("%.1f", actual)),
			plain(fmt.Sprintf("%.1f%%", 100-e.ErrorPct)),
			plain(fmt.Sprintf("%.1f%%", e.ErrorPct)),
			plain(fmt.Sprintf("%d", e.Status2xx)),
			plain(fmt.Sprintf("%d", e.Status4xx)),
			{text: fmt.Sprintf("%d", e.Status5xx), style: styleIf(e.Status5xx > 0, styleRed)},
			{text: fmt.Sprintf("%d", e.Status429), style: styleIf(e.Status429 > 0, styleYellow)},
			plain(fmt.Sprintf("%.0fms", e.P50Ms)),
			plain(fmt.Sprintf("%.0fms", e.P95Ms)),
			plain(fmt.Sprintf("%.0fms", e.P99Ms)),
		})
	}
	writeTable(out, color, header, rows)
}

func writeBucketTable(out io.Writer, color bool, heading string, buckets []BucketStat) {
	header := []string{heading, "REQUESTS", "SHARE", "ERRORS"}
	rows := make([][]cell, 0, len(buckets))
	for _, b := range buckets {
		rows = append(rows, []cell{
			plain(b.Name),
			plain(fmt.Sprintf("%d", b.Requests)),
			plain(fmt.Sprintf("%.1f%%", b.SharePct)),
			plain(fmt.Sprintf("%.1f%%", b.ErrorPct)),
		})
	}
	writeTable(out, color, header, rows)
}

func styleIf(condition bool, style string) string {
	if condition {
		return style
	}
	return ""
}

func formatElapsed(d time.Duration) string {
	total := int(d.Seconds())
	return fmt.Sprintf("%02d:%02d:%02d", total/3600, (total%3600)/60, total%60)
}
