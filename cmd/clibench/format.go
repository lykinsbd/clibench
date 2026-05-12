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
