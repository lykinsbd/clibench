//go:build !linux

package netem

import (
	"fmt"
	"time"
)

// Config holds the netem parameters for a benchmark run.
type Config struct {
	WANDelay    time.Duration
	CampusDelay time.Duration
	WANPorts    []int
	CampusPorts []int
	Jitter      time.Duration
	Loss        float64
}

// Setup returns an error on non-Linux platforms.
func Setup(_ Config) error {
	return fmt.Errorf("tc netem requires Linux")
}

// Teardown is a no-op on non-Linux platforms.
func Teardown() {}
