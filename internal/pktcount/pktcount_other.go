//go:build !linux

package pktcount

import "fmt"

// Counter is unavailable on non-Linux platforms.
type Counter struct{}

// New returns an error on non-Linux platforms.
func New(ports []int) (*Counter, error) {
	return nil, fmt.Errorf("AF_PACKET requires Linux")
}

// Start is a no-op on non-Linux.
func (c *Counter) Start() {}

// Snapshot returns zero on non-Linux.
func (c *Counter) Snapshot() (in, out int) { return 0, 0 }

// Reset is a no-op on non-Linux.
func (c *Counter) Reset() {}

// Stop is a no-op on non-Linux.
func (c *Counter) Stop() {}
