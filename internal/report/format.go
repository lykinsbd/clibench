package report

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// Options controls report rendering.
type Options struct {
	Format string // "table" or "markdown"
	Bars   bool   // render ASCII bar charts (table format only)
}

// barWidth is the maximum width of an ASCII bar.
const barWidth = 24

// Render writes the report to w in the requested format.
func (r Report) Render(w io.Writer, opts Options) error {
	switch opts.Format {
	case "markdown":
		return r.renderMarkdown(w)
	default:
		return r.renderTable(w, opts.Bars)
	}
}

// header returns the report header line describing the run parameters.
func (r Report) header() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%.0fms RTT)", r.Profile, r.RTTms)
	if r.JitterMs > 0 {
		fmt.Fprintf(&b, ", %.0fms jitter", r.JitterMs)
	}
	if r.LossPct > 0 {
		fmt.Fprintf(&b, ", %.2f%% loss", r.LossPct)
	}
	fmt.Fprintf(&b, " — %d iterations, %d commands", r.Iterations, r.Commands)
	return b.String()
}

func (r Report) renderTable(w io.Writer, bars bool) error {
	fmt.Fprintf(w, "=== %s ===\n", r.header())

	maxP50 := r.maxP50()
	for _, g := range r.Groups {
		fmt.Fprintf(w, "\n%s (p50):\n", strings.ToUpper(string(g.Category)))
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		for _, row := range g.Rows {
			label := fmt.Sprintf("%s %s", row.Transport, row.Operation)
			if bars && maxP50 > 0 {
				n := int(row.P50Ms / maxP50 * barWidth)
				bar := strings.Repeat("#", n)
				fmt.Fprintf(tw, "  %s\t%.1fms\t(%d RT)\t%s\n", label, row.P50Ms, row.RoundTrips, bar)
			} else {
				fmt.Fprintf(tw, "  %s\t%.1fms\t(%d RT)\n", label, row.P50Ms, row.RoundTrips)
			}
		}
		if err := tw.Flush(); err != nil {
			return err
		}
	}
	return nil
}

func (r Report) renderMarkdown(w io.Writer) error {
	fmt.Fprintf(w, "## %s\n", r.header())

	for _, g := range r.Groups {
		fmt.Fprintf(w, "\n### %s\n\n", g.Category)
		fmt.Fprintln(w, "| Transport | Operation | p50 (ms) | p95 (ms) | Round Trips |")
		fmt.Fprintln(w, "|---|---|---|---|---|")
		for _, row := range g.Rows {
			fmt.Fprintf(w, "| %s | %s | %.1f | %.1f | %d |\n",
				row.Transport, row.Operation, row.P50Ms, row.P95Ms, row.RoundTrips)
		}
	}
	return nil
}
