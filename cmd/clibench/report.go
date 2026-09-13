package main

import (
	"os"
	"strings"

	"github.com/lykinsbd/clibench/internal/report"
)

// ReportCmd generates a comparison report from a benchmark result JSON file.
type ReportCmd struct {
	File   string `arg:"" help:"Benchmark result JSON file to report on." type:"existingfile"`
	Format string `help:"Output format (${enum})." enum:"table,markdown" default:"table" short:"f"`
	Focus  string `help:"Comma-separated transports to include (default: all)."`
	Bars   bool   `help:"Render ASCII bar charts (table format only)."`
}

// Run executes the report command.
func (r *ReportCmd) Run() error {
	results, err := report.Load(r.File)
	if err != nil {
		return err
	}

	focus := map[string]bool{}
	if r.Focus != "" {
		for _, t := range strings.Split(r.Focus, ",") {
			focus[strings.TrimSpace(t)] = true
		}
	}

	rep := report.Build(results, focus)
	return rep.Render(os.Stdout, report.Options{Format: r.Format, Bars: r.Bars})
}
