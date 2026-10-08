package bench

import (
	"net"
	"testing"
)

func TestSweepSSH(t *testing.T) {
	sshAddr, _ := setupServers(t)
	levels := []int{1, 2, 4}
	results := Sweep("ssh", baseCfg(sshAddr), levels)
	if len(results) != len(levels) {
		t.Fatalf("expected %d sweep results, got %d", len(levels), len(results))
	}
	for i, r := range results {
		if r.Transport != "ssh" {
			t.Errorf("result %d: expected transport ssh, got %q", i, r.Transport)
		}
		if r.Operation != "sweep" {
			t.Errorf("result %d: expected operation sweep, got %q", i, r.Operation)
		}
		if r.SweepN != levels[i] {
			t.Errorf("result %d: expected SweepN %d, got %d", i, levels[i], r.SweepN)
		}
		if r.Errors > 0 {
			t.Errorf("sweep N=%d: %d errors", r.SweepN, r.Errors)
		}
		if r.AvgMs <= 0 {
			t.Errorf("sweep N=%d: avg_ms should be > 0, got %f", r.SweepN, r.AvgMs)
		}
		if r.SetupMs <= 0 {
			t.Errorf("sweep N=%d: setup_ms should be > 0, got %f", r.SweepN, r.SetupMs)
		}
		if r.WallMs <= 0 {
			t.Errorf("sweep N=%d: wall_ms should be > 0, got %f", r.SweepN, r.WallMs)
		}
		if r.OpsPerSec <= 0 {
			t.Errorf("sweep N=%d: ops_per_sec should be > 0, got %f", r.SweepN, r.OpsPerSec)
		}
		if r.Iterations != r.SweepN {
			t.Errorf("sweep N=%d: iterations (%d) should equal held connections", r.SweepN, r.Iterations)
		}
	}
}

func TestSweepHTTPS(t *testing.T) {
	_, httpsAddr := setupServers(t)
	levels := []int{1, 3}
	results := Sweep("https", baseCfg(httpsAddr), levels)
	if len(results) != len(levels) {
		t.Fatalf("expected %d sweep results, got %d", len(levels), len(results))
	}
	for i, r := range results {
		if r.SweepN != levels[i] {
			t.Errorf("result %d: expected SweepN %d, got %d", i, levels[i], r.SweepN)
		}
		if r.Errors > 0 {
			t.Errorf("sweep N=%d: %d errors", r.SweepN, r.Errors)
		}
		if r.OpsPerSec <= 0 {
			t.Errorf("sweep N=%d: ops_per_sec should be > 0", r.SweepN)
		}
	}
}

func TestSweepUnsupportedTransport(t *testing.T) {
	if got := Sweep("netconf", baseCfg("127.0.0.1:1"), []int{1}); got != nil {
		t.Errorf("expected nil for unsupported transport, got %d results", len(got))
	}
}

func TestSweepBadAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	results := Sweep("ssh", baseCfg(addr), []int{2})
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	r := results[0]
	// All connections failed to open: the level still reports, with errors and
	// setup timing, but no successful ops.
	if r.SweepN != 2 {
		t.Errorf("expected SweepN 2, got %d", r.SweepN)
	}
	if r.Errors == 0 && r.AvgMs > 0 {
		t.Errorf("expected a failed sweep level against a closed port, got avg_ms=%f errors=%d", r.AvgMs, r.Errors)
	}
}

func TestSweepSkipsNonPositiveLevels(t *testing.T) {
	sshAddr, _ := setupServers(t)
	results := Sweep("ssh", baseCfg(sshAddr), []int{0, 1, -5})
	if len(results) != 1 {
		t.Fatalf("expected non-positive levels skipped (1 result), got %d", len(results))
	}
	if results[0].SweepN != 1 {
		t.Errorf("expected the single valid level N=1, got %d", results[0].SweepN)
	}
}
