package netem

import (
	"os"
	"testing"
	"time"
)

func TestNetemSetupUnprivileged(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("test only meaningful as non-root")
	}
	err := Setup(Config{
		WANDelay:    10 * time.Millisecond,
		CampusDelay: 1 * time.Millisecond,
		WANPorts:    []int{19999},
		CampusPorts: []int{19998},
	})
	if err == nil {
		Teardown()
		t.Error("expected error from unprivileged Setup")
	}
}
