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
	err := Setup(10*time.Millisecond, 1*time.Millisecond, []int{19999}, []int{19998})
	if err == nil {
		Teardown()
		t.Error("expected error from unprivileged Setup")
	}
}
