// Package testutil provides shared test helpers for creating devices
// and waiting for server readiness.
package testutil

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
)

// NewDevice creates a test device with standard transcripts in a temp dir.
func NewDevice(t *testing.T) *device.Device {
	t.Helper()
	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write("show_version.txt", "TestOS v1\n")
	write("show_ip_interface_brief.txt", "Gi0/0 10.0.0.1\n")
	write("terminal_length_0.txt", "")
	write("terminal_width_511.txt", "")
	dev, err := device.New("test-rtr", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	return dev
}

// WaitTCP polls addr until it accepts a connection or the test times out.
func WaitTCP(t *testing.T, addr string) {
	t.Helper()
	for i := 0; i < 100; i++ {
		c, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			c.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("server %s not ready", addr)
}
