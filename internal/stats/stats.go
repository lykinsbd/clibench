// Package stats provides statistical functions for benchmark results.
package stats

import (
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"sync"
	"time"
)

// ErrDuration is a sentinel value indicating a failed iteration.
const ErrDuration = time.Duration(-1)

// Result holds the statistical summary of a benchmark run.
type Result struct {
	Transport   string  `json:"transport"`
	Operation   string  `json:"operation"`
	Commands    int     `json:"commands"`
	Iterations  int     `json:"iterations"`
	Errors      int     `json:"errors"`
	Concurrency int     `json:"concurrency"`
	Latency     string  `json:"latency_profile"`
	RTTms       float64 `json:"simulated_rtt_ms"`
	AvgMs       float64 `json:"avg_ms"`
	MinMs       float64 `json:"min_ms"`
	MaxMs       float64 `json:"max_ms"`
	P50Ms       float64 `json:"p50_ms"`
	P95Ms       float64 `json:"p95_ms"`
	StddevMs    float64 `json:"stddev_ms"`
}

// Summarize computes statistics from a slice of durations.
func Summarize(transport, op string, cmds, iterations, concurrency int, profile string, rttMs float64, times []time.Duration) Result {
	valid := make([]float64, 0, len(times))
	errors := 0
	for _, t := range times {
		if t == ErrDuration {
			errors++
			continue
		}
		valid = append(valid, float64(t.Microseconds())/1000)
	}
	if errors > 0 {
		log.Printf("  %s/%s: %d/%d iterations failed", transport, op, errors, iterations)
	}
	if len(valid) == 0 {
		return Result{
			Transport: transport, Operation: op, Commands: cmds,
			Iterations: iterations, Errors: errors, Concurrency: concurrency,
			Latency: profile, RTTms: rttMs,
		}
	}

	sort.Float64s(valid)
	n := len(valid)

	var sum float64
	for _, v := range valid {
		sum += v
	}
	avg := sum / float64(n)

	var variance float64
	for _, v := range valid {
		d := v - avg
		variance += d * d
	}
	var stddev float64
	if n > 1 {
		stddev = math.Sqrt(variance / float64(n-1))
	}

	return Result{
		Transport:   transport,
		Operation:   op,
		Commands:    cmds,
		Iterations:  iterations,
		Errors:      errors,
		Concurrency: concurrency,
		Latency:     profile,
		RTTms:       rttMs,
		AvgMs:       avg,
		MinMs:       valid[0],
		MaxMs:       valid[n-1],
		P50Ms:       Percentile(valid, 50),
		P95Ms:       Percentile(valid, 95),
		StddevMs:    stddev,
	}
}

// Percentile returns the p-th percentile from a sorted slice.
func Percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	rank := (p / 100) * float64(len(sorted)-1)
	lower := int(rank)
	upper := lower + 1
	if upper >= len(sorted) {
		return sorted[len(sorted)-1]
	}
	frac := rank - float64(lower)
	return sorted[lower] + frac*(sorted[upper]-sorted[lower])
}

// RunParallel executes fn iterations times with the given concurrency.
func RunParallel(iterations, concurrency int, fn func() time.Duration) []time.Duration {
	results := make([]time.Duration, iterations)
	sem := make(chan struct{}, concurrency)
	var mu sync.Mutex
	var wg sync.WaitGroup

	for i := 0; i < iterations; i++ {
		wg.Add(1)
		sem <- struct{}{}
		go func(idx int) {
			defer wg.Done()
			defer func() { <-sem }()
			d := fn()
			mu.Lock()
			results[idx] = d
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	return results
}

// GenerateExecPayload creates a newline-delimited string of N show commands.
func GenerateExecPayload(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		fmt.Fprintf(&b, "show version\n")
	}
	return b.String()
}
