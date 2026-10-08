package report

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"github.com/lykinsbd/clibench/internal/stats"
)

// Row is a single benchmark result in a report, flattened for display.
type Row struct {
	Transport string
	Operation string
	P50Ms     float64
	P95Ms     float64
	RoundTrips int
}

// Group is a set of rows sharing a category, sorted by p50 latency.
type Group struct {
	Category Category
	Rows     []Row
}

// Report is the parsed, grouped view of a single result file.
type Report struct {
	Profile    string
	RTTms      float64
	JitterMs   float64
	LossPct    float64
	Iterations int
	Commands   int
	Groups     []Group
}

// Load reads a result JSON file and returns the raw results.
func Load(path string) ([]stats.Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var results []stats.Result
	if err := json.Unmarshal(data, &results); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("%s: no results found", path)
	}
	return results, nil
}

// Build groups results by category and ranks each group by p50 latency.
// If focus is non-empty, only transports in the set are included.
func Build(results []stats.Result, focus map[string]bool) Report {
	rep := Report{}
	if len(results) > 0 {
		rep.Profile = results[0].Latency
		rep.RTTms = results[0].RTTms
		rep.JitterMs = results[0].JitterMs
		rep.LossPct = results[0].LossPct
		rep.Iterations = results[0].Iterations
		rep.Commands = results[0].Commands
	}

	byCat := map[Category][]Row{}
	for _, r := range results {
		if len(focus) > 0 && !focus[r.Transport] {
			continue
		}
		cat := Classify(r.Operation)
		byCat[cat] = append(byCat[cat], Row{
			Transport:  r.Transport,
			Operation:  r.Operation,
			P50Ms:      r.P50Ms,
			P95Ms:      r.P95Ms,
			RoundTrips: r.RoundTrips,
		})
	}

	for _, cat := range CategoryOrder {
		rows := byCat[cat]
		if len(rows) == 0 {
			continue
		}
		sort.Slice(rows, func(i, j int) bool {
			return rows[i].P50Ms < rows[j].P50Ms
		})
		rep.Groups = append(rep.Groups, Group{Category: cat, Rows: rows})
	}
	return rep
}

// maxP50 returns the largest p50 across all rows in the report (for bar scaling).
func (r Report) maxP50() float64 {
	var max float64
	for _, g := range r.Groups {
		for _, row := range g.Rows {
			if row.P50Ms > max {
				max = row.P50Ms
			}
		}
	}
	return max
}
