// Package report generates human-readable comparison tables from clibench
// benchmark result JSON files. It groups benchmark modes into categories,
// ranks them by latency, and formats the output as tables or markdown.
package report

import "strings"

// Category classifies a benchmark mode by its connection-reuse pattern.
type Category string

const (
	// Fresh: a new connection/session is established every iteration.
	Fresh Category = "Fresh connection"
	// Reuse: a connection is established once and reused per command.
	Reuse Category = "Keep-alive / reuse"
	// Batch: all commands are sent in a single operation.
	Batch Category = "Batch"
	// Other: modes that don't fit the above (e.g. tunnel, discovery).
	Other Category = "Other"
)

// CategoryOrder is the display order for categories.
var CategoryOrder = []Category{Fresh, Reuse, Batch, Other}

// Classify maps a benchmark operation name to a Category.
// The classification is based on operation-name substrings, matching the
// naming conventions used across all transports.
func Classify(operation string) Category {
	op := strings.ToLower(operation)

	// Batch modes: all commands in one operation.
	switch {
	case strings.Contains(op, "batch"),
		strings.Contains(op, "multi-cmd"),
		op == "edit-commit",
		op == "subscribe-once":
		return Batch
	}

	// Fresh modes: new connection/session per iteration.
	switch {
	case strings.Contains(op, "fresh"),
		op == "0rtt-resumption":
		return Fresh
	}

	// Reuse modes: shared connection, per-command operation.
	switch {
	case strings.Contains(op, "reuse"),
		strings.Contains(op, "keep-alive"),
		strings.Contains(op, "pooled"),
		op == "json-vs-xml":
		return Reuse
	}

	// Tunnel modes and anything else.
	return Other
}
