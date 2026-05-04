package stats

import (
	"math"
	"sync/atomic"
	"testing"
	"time"
)

func TestPercentile(t *testing.T) {
	sorted := []float64{10, 20, 30, 40, 50}
	tests := []struct {
		p    float64
		want float64
	}{
		{0, 10},
		{50, 30},
		{100, 50},
	}
	for _, tt := range tests {
		got := Percentile(sorted, tt.p)
		if got != tt.want {
			t.Errorf("Percentile(p=%v) = %v, want %v", tt.p, got, tt.want)
		}
	}
	// single element
	if got := Percentile([]float64{42}, 50); got != 42 {
		t.Errorf("single element p50 = %v, want 42", got)
	}
	// empty slice
	if got := Percentile(nil, 50); got != 0 {
		t.Errorf("empty p50 = %v, want 0", got)
	}
}

func TestPercentileInterpolation(t *testing.T) {
	sorted := []float64{10, 20, 30, 40, 50}
	// p95: rank = 0.95 * 4 = 3.8 → 40 + 0.8*(50-40) = 48
	got := Percentile(sorted, 95)
	if math.Abs(got-48) > 0.001 {
		t.Errorf("p95 = %v, want 48", got)
	}
}

func TestPercentileKnownValues(t *testing.T) {
	sorted := []float64{10, 20, 30, 40, 50}
	if got := Percentile(sorted, 50); got != 30 {
		t.Errorf("p50 = %v, want 30", got)
	}
	if got := Percentile(sorted, 95); math.Abs(got-48) > 0.001 {
		t.Errorf("p95 = %v, want 48", got)
	}
}

func TestSummarizeBasic(t *testing.T) {
	times := []time.Duration{2 * time.Millisecond, 4 * time.Millisecond, 6 * time.Millisecond}
	r := Summarize("t", "op", 1, 3, 1, "local", 0, times)
	if r.Errors != 0 {
		t.Errorf("errors = %d", r.Errors)
	}
	if math.Abs(r.AvgMs-4) > 0.01 {
		t.Errorf("avg = %v, want 4", r.AvgMs)
	}
	if r.MinMs != 2 {
		t.Errorf("min = %v, want 2", r.MinMs)
	}
	if r.MaxMs != 6 {
		t.Errorf("max = %v, want 6", r.MaxMs)
	}
}

func TestStddevKnownValues(t *testing.T) {
	// [2,4,4,4,5,5,7,9] ms → mean=5, sum of squared deviations=32
	// sample stddev = sqrt(32/7) ≈ 2.1381
	times := make([]time.Duration, 8)
	vals := []int{2, 4, 4, 4, 5, 5, 7, 9}
	for i, v := range vals {
		times[i] = time.Duration(v) * time.Millisecond
	}
	r := Summarize("t", "op", 1, 8, 1, "local", 0, times)
	want := math.Sqrt(32.0 / 7.0) // ≈ 2.1381
	if math.Abs(r.StddevMs-want) > 0.01 {
		t.Errorf("stddev = %v, want %v", r.StddevMs, want)
	}
}

func TestSummarizeAllErrors(t *testing.T) {
	times := []time.Duration{ErrDuration, ErrDuration, ErrDuration}
	r := Summarize("t", "op", 1, 3, 1, "local", 0, times)
	if r.Errors != 3 {
		t.Errorf("errors = %d, want 3", r.Errors)
	}
	if r.AvgMs != 0 || r.MinMs != 0 || r.MaxMs != 0 {
		t.Errorf("expected zero stats, got avg=%v min=%v max=%v", r.AvgMs, r.MinMs, r.MaxMs)
	}
}

func TestSummarizePartialErrors(t *testing.T) {
	times := []time.Duration{ErrDuration, 2 * time.Millisecond, 4 * time.Millisecond}
	r := Summarize("t", "op", 1, 3, 1, "local", 0, times)
	if r.Errors != 1 {
		t.Errorf("errors = %d, want 1", r.Errors)
	}
	if math.Abs(r.AvgMs-3) > 0.01 {
		t.Errorf("avg = %v, want 3", r.AvgMs)
	}
}

func TestErrDurationSentinel(t *testing.T) {
	times := []time.Duration{ErrDuration, 5 * time.Millisecond, ErrDuration, 10 * time.Millisecond}
	r := Summarize("t", "op", 1, 4, 1, "local", 0, times)
	if r.Errors != 2 {
		t.Errorf("errors = %d, want 2", r.Errors)
	}
	if math.Abs(r.AvgMs-7.5) > 0.01 {
		t.Errorf("avg = %v, want 7.5 (from valid only)", r.AvgMs)
	}
}

func TestGenerateExecPayload(t *testing.T) {
	p := GenerateExecPayload(3)
	want := "show version\nshow version\nshow version\n"
	if p != want {
		t.Errorf("got %q, want %q", p, want)
	}
}

func TestRunParallelConcurrency(t *testing.T) {
	// With concurrency=1, iterations should execute serially (no overlap)
	var running atomic.Int32
	var maxRunning atomic.Int32
	results := RunParallel(5, 1, func(_ int) time.Duration {
		cur := running.Add(1)
		for {
			old := maxRunning.Load()
			if cur <= old || maxRunning.CompareAndSwap(old, cur) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		running.Add(-1)
		return time.Millisecond
	})
	if len(results) != 5 {
		t.Errorf("got %d results, want 5", len(results))
	}
	if maxRunning.Load() > 1 {
		t.Errorf("max concurrent = %d, want 1", maxRunning.Load())
	}
}

func TestRunParallelCountsErrors(t *testing.T) {
	results := RunParallel(4, 1, func(_ int) time.Duration {
		return ErrDuration
	})
	errCount := 0
	for _, d := range results {
		if d == ErrDuration {
			errCount++
		}
	}
	if errCount != 4 {
		t.Errorf("errCount = %d, want 4", errCount)
	}
}

func TestSummarizeWithTrips(t *testing.T) {
	times := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 15 * time.Millisecond}
	ic := IterCounts{
		Trips:  []int{5, 7, 5},
		Reads:  []int{10, 12, 10},
		Writes: []int{8, 9, 8},
	}
	r := Summarize("ssh", "fresh-conn", 1, 3, 1, "local", 0, times, ic)
	if r.RoundTrips != 5 { // median of [5, 5, 7] = 5
		t.Errorf("RoundTrips = %d, want 5", r.RoundTrips)
	}
	if r.ReadOps != 10 { // median of [10, 10, 12] = 10
		t.Errorf("ReadOps = %d, want 10", r.ReadOps)
	}
	if r.WriteOps != 8 { // median of [8, 8, 9] = 8
		t.Errorf("WriteOps = %d, want 8", r.WriteOps)
	}
}

func TestSummarizeWithoutTrips(t *testing.T) {
	times := []time.Duration{10 * time.Millisecond}
	r := Summarize("https", "keep-alive", 1, 1, 1, "local", 0, times)
	if r.RoundTrips != 0 {
		t.Errorf("RoundTrips = %d, want 0 when no counts provided", r.RoundTrips)
	}
	if r.ReadOps != 0 {
		t.Errorf("ReadOps = %d, want 0 when no counts provided", r.ReadOps)
	}
}
