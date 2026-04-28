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

func TestSetupTeardown(t *testing.T) {
	requireRoot(t)
	lo, _ := netlink.LinkByIndex(loopbackIndex)
	err := Setup(10*time.Millisecond, 1*time.Millisecond, []int{19999}, []int{19998})
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
	if err := Setup(10*time.Millisecond, 1*time.Millisecond, []int{19999}, []int{19998}); err != nil {
		t.Fatal(err)
	}
	// Second call should not error (Teardown called internally)
	if err := Setup(10*time.Millisecond, 1*time.Millisecond, []int{19999}, []int{19998}); err != nil {
		t.Errorf("second Setup failed: %v", err)
	}
}

func TestTeardownNoOp(t *testing.T) {
	requireRoot(t)
	// Teardown on clean interface should not panic
	Teardown()
	Teardown()
}
