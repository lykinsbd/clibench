//go:build !linux

package netem

import (
	"fmt"
	"time"
)

// Setup returns an error on non-Linux platforms.
func Setup(wanDelay, campusDelay time.Duration, wanPorts, campusPorts []int) error {
	return fmt.Errorf("tc netem requires Linux")
}

// Teardown is a no-op on non-Linux platforms.
func Teardown() {}
