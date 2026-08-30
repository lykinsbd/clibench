//go:build netem_root

package netem

import (
	"os"
	"testing"
	"time"

	"github.com/vishvananda/netlink"
)

func requireRoot(t *testing.T) {
	t.Helper()
	if os.Getuid() != 0 {
		t.Skip("requires root")
	}
}

func testConfig() Config {
	return Config{
		WANDelay:    10 * time.Millisecond,
		CampusDelay: 1 * time.Millisecond,
		WANPorts:    []int{19999},
		CampusPorts: []int{19998},
	}
}

func TestSetupTeardown(t *testing.T) {
	requireRoot(t)
	lo, _ := netlink.LinkByIndex(loopbackIndex)
	err := Setup(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	qdiscs, _ := netlink.QdiscList(lo)
	found := false
	for _, q := range qdiscs {
		if q.Attrs().Handle == netlink.MakeHandle(1, 0) {
			found = true
		}
	}
	if !found {
		t.Error("prio qdisc not found after Setup")
	}
	Teardown()
	qdiscs, _ = netlink.QdiscList(lo)
	for _, q := range qdiscs {
		if q.Attrs().Handle == netlink.MakeHandle(1, 0) {
			t.Error("prio qdisc still present after Teardown")
		}
	}
}

func TestSetupIdempotent(t *testing.T) {
	requireRoot(t)
	defer Teardown()
	if err := Setup(testConfig()); err != nil {
		t.Fatal(err)
	}
	// Second call should not error (Teardown called internally)
	if err := Setup(testConfig()); err != nil {
		t.Errorf("second Setup failed: %v", err)
	}
}

func TestSetupWithJitterAndLoss(t *testing.T) {
	requireRoot(t)
	defer Teardown()
	cfg := testConfig()
	cfg.Jitter = 5 * time.Millisecond
	cfg.Loss = 1.0
	if err := Setup(cfg); err != nil {
		t.Fatalf("Setup with jitter+loss failed: %v", err)
	}
	// Verify the netem qdisc exists on the WAN band.
	lo, _ := netlink.LinkByIndex(loopbackIndex)
	qdiscs, _ := netlink.QdiscList(lo)
	found := false
	for _, q := range qdiscs {
		if _, ok := q.(*netlink.Netem); ok {
			found = true
		}
	}
	if !found {
		t.Error("netem qdisc not found after Setup with jitter+loss")
	}
}

func TestTeardownNoOp(t *testing.T) {
	requireRoot(t)
	// Teardown on clean interface should not panic
	Teardown()
	Teardown()
}
