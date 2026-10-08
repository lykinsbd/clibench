package main

import (
	"encoding/csv"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/lykinsbd/clibench/internal/stats"
)

func writeTable(w io.Writer, results []stats.Result) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TRANSPORT\tOPERATION\tCMDS\tITER\tERR\tRT\tRD_OPS\tWR_OPS\tPKT_IN\tPKT_OUT\tCPU_US\tALLOC_B\tALLOCS\tAVG(ms)\tMIN(ms)\tP50(ms)\tP95(ms)\tMAX(ms)\tSTDDEV(ms)")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%.2f\t%.2f\t%.2f\t%.2f\t%.2f\t%.2f\n",
			r.Transport, r.Operation, r.Commands, r.Iterations, r.Errors,
			r.RoundTrips, r.ReadOps, r.WriteOps, r.PacketsIn, r.PacketsOut,
			r.CPUUs, r.AllocBytes, r.Allocs,
			r.AvgMs, r.MinMs, r.P50Ms, r.P95Ms, r.MaxMs, r.StddevMs)
	}
	return tw.Flush()
}

func writeCSV(w io.Writer, results []stats.Result) error {
	cw := csv.NewWriter(w)
	// Column order matches table: avg, min, p50, p95, max, stddev
	_ = cw.Write([]string{
		"transport", "operation", "commands", "iterations", "errors",
		"concurrency", "latency_profile", "simulated_rtt_ms",
		"round_trips", "read_ops", "write_ops", "packets_in", "packets_out",
		"cpu_us", "alloc_bytes", "allocs",
		"avg_ms", "min_ms", "p50_ms", "p95_ms", "max_ms", "stddev_ms",
	})
	for _, r := range results {
		_ = cw.Write([]string{
			r.Transport, r.Operation,
			fmt.Sprintf("%d", r.Commands), fmt.Sprintf("%d", r.Iterations),
			fmt.Sprintf("%d", r.Errors), fmt.Sprintf("%d", r.Concurrency),
			r.Latency, fmt.Sprintf("%.1f", r.RTTms),
			fmt.Sprintf("%d", r.RoundTrips),
			fmt.Sprintf("%d", r.ReadOps), fmt.Sprintf("%d", r.WriteOps),
			fmt.Sprintf("%d", r.PacketsIn), fmt.Sprintf("%d", r.PacketsOut),
			fmt.Sprintf("%d", r.CPUUs), fmt.Sprintf("%d", r.AllocBytes),
			fmt.Sprintf("%d", r.Allocs),
			fmt.Sprintf("%.3f", r.AvgMs), fmt.Sprintf("%.3f", r.MinMs),
			fmt.Sprintf("%.3f", r.P50Ms), fmt.Sprintf("%.3f", r.P95Ms),
			fmt.Sprintf("%.3f", r.MaxMs), fmt.Sprintf("%.3f", r.StddevMs),
		})
	}
	cw.Flush()
	return cw.Error()
}

// isSweep reports whether these results came from the concurrency-sweep mode.
func isSweep(results []stats.Result) bool {
	for _, r := range results {
		if r.Operation == "sweep" {
			return true
		}
	}
	return false
}

// writeSweepTable renders the connection-scaling columns that matter for a
// hold-open sweep (issue #49): concurrency level, setup time, wall time,
// per-connection p50/p95, ops/sec, and live heap held.
func writeSweepTable(w io.Writer, results []stats.Result) error {
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TRANSPORT\tCONCURRENT\tCMDS\tERR\tSETUP(ms)\tWALL(ms)\tP50(ms)\tP95(ms)\tOPS/sec\tLIVE_HEAP(MB)")
	for _, r := range results {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%d\t%.2f\t%.2f\t%.2f\t%.2f\t%.1f\t%.2f\n",
			r.Transport, r.SweepN, r.Commands, r.Errors,
			r.SetupMs, r.WallMs, r.P50Ms, r.P95Ms, r.OpsPerSec, r.LiveHeapMB)
	}
	return tw.Flush()
}

// writeSweepCSV is the CSV counterpart to writeSweepTable.
func writeSweepCSV(w io.Writer, results []stats.Result) error {
	cw := csv.NewWriter(w)
	_ = cw.Write([]string{
		"transport", "concurrency", "commands", "errors",
		"latency_profile", "simulated_rtt_ms",
		"setup_ms", "wall_ms", "p50_ms", "p95_ms", "ops_per_sec", "live_heap_mb",
	})
	for _, r := range results {
		_ = cw.Write([]string{
			r.Transport, fmt.Sprintf("%d", r.SweepN),
			fmt.Sprintf("%d", r.Commands), fmt.Sprintf("%d", r.Errors),
			r.Latency, fmt.Sprintf("%.1f", r.RTTms),
			fmt.Sprintf("%.3f", r.SetupMs), fmt.Sprintf("%.3f", r.WallMs),
			fmt.Sprintf("%.3f", r.P50Ms), fmt.Sprintf("%.3f", r.P95Ms),
			fmt.Sprintf("%.3f", r.OpsPerSec), fmt.Sprintf("%.3f", r.LiveHeapMB),
		})
	}
	cw.Flush()
	return cw.Error()
}
