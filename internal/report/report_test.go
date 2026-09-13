package report_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lykinsbd/clibench/internal/report"
	"github.com/lykinsbd/clibench/internal/stats"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		op   string
		want report.Category
	}{
		// Fresh
		{"fresh-conn", report.Fresh},
		{"fresh-session", report.Fresh},
		{"fresh-ssh", report.Fresh},
		{"pty-fresh", report.Fresh},
		{"h3-fresh-ssh", report.Fresh},
		{"0rtt-resumption", report.Fresh},
		// Reuse
		{"reuse-conn", report.Reuse},
		{"reuse-session", report.Reuse},
		{"reuse-stream", report.Reuse},
		{"keep-alive", report.Reuse},
		{"h3-keep-alive", report.Reuse},
		{"pty-reuse", report.Reuse},
		{"pooled-ssh", report.Reuse},
		{"h3-pooled-ssh", report.Reuse},
		{"json-vs-xml", report.Reuse},
		// Batch
		{"batch-exec", report.Batch},
		{"batch-post", report.Batch},
		{"batch-rpc", report.Batch},
		{"batch-set", report.Batch},
		{"batch-patch", report.Batch},
		{"multi-cmd", report.Batch},
		{"edit-commit", report.Batch},
		{"subscribe-once", report.Batch},
		{"ssh-https-ssh-batch", report.Batch},
		// Other
		{"ssh-https-ssh", report.Other},
		{"ssh-http3-ssh", report.Other},
	}
	for _, tt := range tests {
		got := report.Classify(tt.op)
		if got != tt.want {
			t.Errorf("Classify(%q) = %q, want %q", tt.op, got, tt.want)
		}
	}
}

func writeResults(t *testing.T, results []stats.Result) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "results.json")
	data, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func sampleResults() []stats.Result {
	return []stats.Result{
		{Transport: "ssh", Operation: "fresh-conn", Latency: "regional", RTTms: 30, Iterations: 20, Commands: 5, P50Ms: 493, RoundTrips: 16},
		{Transport: "https", Operation: "fresh-conn", Latency: "regional", RTTms: 30, Iterations: 20, Commands: 5, P50Ms: 476, RoundTrips: 10},
		{Transport: "https", Operation: "keep-alive", Latency: "regional", RTTms: 30, Iterations: 20, Commands: 5, P50Ms: 155, RoundTrips: 5},
		{Transport: "https", Operation: "batch-post", Latency: "regional", RTTms: 30, Iterations: 20, Commands: 5, P50Ms: 31, RoundTrips: 1},
		{Transport: "ssh", Operation: "batch-exec", Latency: "regional", RTTms: 30, Iterations: 20, Commands: 5, P50Ms: 247, RoundTrips: 9},
	}
}

func TestLoad(t *testing.T) {
	path := writeResults(t, sampleResults())
	results, err := report.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(results) != 5 {
		t.Errorf("expected 5 results, got %d", len(results))
	}
}

func TestLoadErrors(t *testing.T) {
	if _, err := report.Load("/nonexistent/file.json"); err == nil {
		t.Error("expected error for missing file")
	}

	dir := t.TempDir()
	empty := filepath.Join(dir, "empty.json")
	os.WriteFile(empty, []byte("[]"), 0644)
	if _, err := report.Load(empty); err == nil {
		t.Error("expected error for empty results")
	}
}

func TestBuildGroupsAndRanks(t *testing.T) {
	rep := report.Build(sampleResults(), nil)

	if rep.Profile != "regional" || rep.RTTms != 30 {
		t.Errorf("metadata wrong: profile=%q rtt=%v", rep.Profile, rep.RTTms)
	}

	// Expect 3 groups: Fresh, Reuse, Batch (no Other in sample).
	if len(rep.Groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(rep.Groups))
	}

	// Groups must be in CategoryOrder: Fresh, Reuse, Batch.
	if rep.Groups[0].Category != report.Fresh {
		t.Errorf("group[0] = %q, want Fresh", rep.Groups[0].Category)
	}
	if rep.Groups[2].Category != report.Batch {
		t.Errorf("group[2] = %q, want Batch", rep.Groups[2].Category)
	}

	// Within Fresh group, rows sorted by p50 ascending: https (476) before ssh (493).
	fresh := rep.Groups[0].Rows
	if fresh[0].Transport != "https" || fresh[1].Transport != "ssh" {
		t.Errorf("fresh group not sorted by p50: %+v", fresh)
	}
}

func TestBuildFocus(t *testing.T) {
	focus := map[string]bool{"https": true}
	rep := report.Build(sampleResults(), focus)

	for _, g := range rep.Groups {
		for _, row := range g.Rows {
			if row.Transport != "https" {
				t.Errorf("focus filter leaked transport %q", row.Transport)
			}
		}
	}
}

func TestRenderTable(t *testing.T) {
	rep := report.Build(sampleResults(), nil)
	var b strings.Builder
	if err := rep.Render(&b, report.Options{Format: "table"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "regional (30ms RTT)") {
		t.Error("missing header")
	}
	if !strings.Contains(out, "FRESH CONNECTION") {
		t.Error("missing Fresh category header")
	}
	if !strings.Contains(out, "batch-post") {
		t.Error("missing batch-post row")
	}
}

func TestRenderMarkdown(t *testing.T) {
	rep := report.Build(sampleResults(), nil)
	var b strings.Builder
	if err := rep.Render(&b, report.Options{Format: "markdown"}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := b.String()
	if !strings.Contains(out, "## regional") {
		t.Error("missing markdown header")
	}
	if !strings.Contains(out, "| Transport | Operation |") {
		t.Error("missing markdown table header")
	}
}

func TestRenderBars(t *testing.T) {
	rep := report.Build(sampleResults(), nil)
	var b strings.Builder
	if err := rep.Render(&b, report.Options{Format: "table", Bars: true}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(b.String(), "#") {
		t.Error("expected bar characters in output")
	}
}

func TestHeaderWithJitterAndLoss(t *testing.T) {
	results := sampleResults()
	for i := range results {
		results[i].JitterMs = 10
		results[i].LossPct = 1.0
	}
	rep := report.Build(results, nil)
	var b strings.Builder
	rep.Render(&b, report.Options{Format: "table"})
	out := b.String()
	if !strings.Contains(out, "10ms jitter") {
		t.Errorf("missing jitter in header: %s", out)
	}
	if !strings.Contains(out, "1.00% loss") {
		t.Errorf("missing loss in header: %s", out)
	}
}
