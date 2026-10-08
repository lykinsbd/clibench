package bench

import (
	"net"
	"testing"

	"github.com/lykinsbd/clibench/internal/netconfserver"
	"github.com/lykinsbd/clibench/internal/restconfserver"
	"github.com/lykinsbd/clibench/internal/stats"
	"github.com/lykinsbd/clibench/internal/testutil"
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

// assertSweepResults checks the common invariants for a successful sweep run.
func assertSweepResults(t *testing.T, transport string, results []stats.Result, levels []int) {
	t.Helper()
	if len(results) != len(levels) {
		t.Fatalf("%s: expected %d sweep results, got %d", transport, len(levels), len(results))
	}
	for i, r := range results {
		if r.Transport != transport {
			t.Errorf("%s result %d: expected transport %q, got %q", transport, i, transport, r.Transport)
		}
		if r.Operation != "sweep" {
			t.Errorf("%s result %d: expected operation sweep, got %q", transport, i, r.Operation)
		}
		if r.SweepN != levels[i] {
			t.Errorf("%s result %d: expected SweepN %d, got %d", transport, i, levels[i], r.SweepN)
		}
		if r.Errors > 0 {
			t.Errorf("%s sweep N=%d: %d errors", transport, r.SweepN, r.Errors)
		}
		if r.SetupMs <= 0 {
			t.Errorf("%s sweep N=%d: setup_ms should be > 0, got %f", transport, r.SweepN, r.SetupMs)
		}
		if r.OpsPerSec <= 0 {
			t.Errorf("%s sweep N=%d: ops_per_sec should be > 0, got %f", transport, r.SweepN, r.OpsPerSec)
		}
	}
}

func setupNETCONFServer(t *testing.T) string {
	t.Helper()
	dev := testutil.NewDevice(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := netconfserver.New(ln.Addr().String(), dev)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetListener(ln)
	go srv.ListenAndServe() //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	testutil.WaitTCP(t, ln.Addr().String())
	return ln.Addr().String()
}

func setupRESTCONFServer(t *testing.T) string {
	t.Helper()
	dev := testutil.NewDevice(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := restconfserver.New(ln.Addr().String(), dev)
	srv.SetListener(ln)
	go srv.ListenAndServeTLS() //nolint:errcheck
	t.Cleanup(func() { srv.Close() })
	testutil.WaitTCP(t, ln.Addr().String())
	return ln.Addr().String()
}

func TestSweepHTTP3(t *testing.T) {
	h3Addr := setupHTTP3Server(t)
	levels := []int{1, 3}
	assertSweepResults(t, "http3", Sweep("http3", baseCfg(h3Addr), levels), levels)
}

func TestSweepNETCONF(t *testing.T) {
	addr := setupNETCONFServer(t)
	levels := []int{1, 2, 4}
	assertSweepResults(t, "netconf", Sweep("netconf", baseCfg(addr), levels), levels)
}

func TestSweepRESTCONF(t *testing.T) {
	addr := setupRESTCONFServer(t)
	levels := []int{1, 3}
	assertSweepResults(t, "restconf", Sweep("restconf", baseCfg(addr), levels), levels)
}

func TestSweepUnsupportedTransport(t *testing.T) {
	// proxy/tunnel are compound topologies, deliberately excluded from the sweep;
	// a bogus name must also return nil.
	for _, tr := range []string{"proxy", "tunnel-https", "bogus"} {
		if got := Sweep(tr, baseCfg("127.0.0.1:1"), []int{1}); got != nil {
			t.Errorf("transport %q: expected nil for unsupported transport, got %d results", tr, len(got))
		}
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
