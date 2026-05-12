// Package resource captures per-iteration CPU and memory usage.
package resource

import (
	"os"
	"runtime"
)

// Snapshot holds resource usage at a point in time.
type Snapshot struct {
	CPUNs  int64
	Allocs uint64
	Bytes  uint64
}

// Now captures current resource usage.
func Now() Snapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return Snapshot{
		CPUNs:  cpuNanos(),
		Allocs: m.Mallocs,
		Bytes:  m.TotalAlloc,
	}
}

// Delta holds the resource difference between two snapshots.
type Delta struct {
	CPUUs  int64  // microseconds of CPU (user+system)
	Allocs uint64 // heap allocation count
	Bytes  uint64 // heap bytes allocated
}

// Since returns the resource delta since s.
func Since(s Snapshot) Delta {
	now := Now()
	return Delta{
		CPUUs:  (now.CPUNs - s.CPUNs) / 1000,
		Allocs: now.Allocs - s.Allocs,
		Bytes:  now.Bytes - s.Bytes,
	}
}

// cpuNanos reads user+system CPU time from /proc/self/stat.
// Returns 0 on non-Linux or on error.
func cpuNanos() int64 {
	data, err := os.ReadFile("/proc/self/stat")
	if err != nil {
		return 0
	}
	// Find closing paren of comm field.
	i := len(data) - 1
	for i >= 0 && data[i] != ')' {
		i--
	}
	if i < 0 {
		return 0
	}
	// After ") X " (state), fields are space-separated.
	// utime is field 14 (0-indexed from start), which is field index 11
	// after the closing paren (fields after ") " start at index 0 = state).
	fields := 0
	var utime, stime int64
	for j := i + 2; j < len(data); j++ {
		if data[j] == ' ' {
			fields++
		} else if fields == 11 {
			for j < len(data) && data[j] >= '0' && data[j] <= '9' {
				utime = utime*10 + int64(data[j]-'0')
				j++
			}
		} else if fields == 12 {
			for j < len(data) && data[j] >= '0' && data[j] <= '9' {
				stime = stime*10 + int64(data[j]-'0')
				j++
			}
			break
		}
	}
	// Clock ticks to nanoseconds. SC_CLK_TCK = 100 on Linux (10ms/tick).
	const tickNs = 10_000_000 // 1e9 / 100
	return (utime + stime) * tickNs
}
