package bench

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/httpserver"
	"github.com/lykinsbd/clibench/internal/proxy"
	"github.com/lykinsbd/clibench/internal/sshserver"
	"github.com/lykinsbd/clibench/internal/stats"
)

func setupServers(t *testing.T) (sshAddr, httpsAddr string) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "show_version.txt"), []byte("TestOS v1\n"), 0644)
	os.WriteFile(filepath.Join(dir, "show_ip_interface_brief.txt"), []byte("Gi0/0 10.0.0.1\n"), 0644)
	os.WriteFile(filepath.Join(dir, "terminal_length_0.txt"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "terminal_width_511.txt"), []byte(""), 0644)

	dev, err := device.New("test-rtr", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}

	sshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	sshSrv, err := sshserver.New(sshLn.Addr().String(), dev)
	if err != nil {
		t.Fatal(err)
	}
	sshSrv.SetListener(sshLn)
	go sshSrv.ListenAndServe()

	httpsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpSrv := httpserver.New(httpsLn.Addr().String(), dev)
	httpSrv.SetListener(httpsLn)
	go httpSrv.ListenAndServeTLS()

	time.Sleep(200 * time.Millisecond)
	return sshLn.Addr().String(), httpsLn.Addr().String()
}

func baseCfg(addr string) Config {
	return Config{
		Addr:        addr,
		User:        "admin",
		Pass:        "admin",
		Iterations:  3,
		Concurrency: 1,
		Commands:    2,
		Profile:     "local",
		RTTms:       0,
		Hostname:    "test-rtr",
	}
}

func assertResults(t *testing.T, results []stats.Result, transport string, minModes int) {
	t.Helper()
	if len(results) < minModes {
		t.Fatalf("expected at least %d results for %s, got %d", minModes, transport, len(results))
	}
	for _, r := range results {
		if r.Transport != transport {
			t.Errorf("expected transport %q, got %q", transport, r.Transport)
		}
		if r.Errors > 0 {
			t.Errorf("%s/%s: %d errors out of %d iterations", r.Transport, r.Operation, r.Errors, r.Iterations)
		}
		if r.AvgMs <= 0 {
			t.Errorf("%s/%s: avg_ms should be > 0, got %f", r.Transport, r.Operation, r.AvgMs)
		}
		if r.RoundTrips <= 0 {
			t.Errorf("%s/%s: round_trips should be > 0, got %d", r.Transport, r.Operation, r.RoundTrips)
		}
		if r.ReadOps <= 0 {
			t.Errorf("%s/%s: read_ops should be > 0, got %d", r.Transport, r.Operation, r.ReadOps)
		}
		if r.WriteOps <= 0 {
			t.Errorf("%s/%s: write_ops should be > 0, got %d", r.Transport, r.Operation, r.WriteOps)
		}
	}
}

func TestSSH(t *testing.T) {
	sshAddr, _ := setupServers(t)
	results := SSH(baseCfg(sshAddr))
	// fresh-conn, reuse-conn, batch-exec, pty-fresh, pty-reuse = 5 modes
	assertResults(t, results, "ssh", 5)
}

func TestHTTPS(t *testing.T) {
	_, httpsAddr := setupServers(t)
	results := HTTPS(baseCfg(httpsAddr))
	// fresh-conn, keep-alive, batch-post, multi-cmd (commands=2 > 1) = 4 modes
	assertResults(t, results, "https", 4)
}

func TestHTTPSSingleCommand(t *testing.T) {
	_, httpsAddr := setupServers(t)
	cfg := baseCfg(httpsAddr)
	cfg.Commands = 1
	results := HTTPS(cfg)
	// With commands=1, multi-cmd is skipped = 3 modes
	assertResults(t, results, "https", 3)
	for _, r := range results {
		if r.Operation == "multi-cmd" {
			t.Error("multi-cmd should not be present with commands=1")
		}
	}
}

func TestProxy(t *testing.T) {
	sshAddr, _ := setupServers(t)

	freshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pFresh := proxy.New(freshLn.Addr().String(), sshAddr, "admin", "admin", false)
	pFresh.SetListener(freshLn)
	go pFresh.ListenAndServeTLS()

	pooledLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	pPooled := proxy.New(pooledLn.Addr().String(), sshAddr, "admin", "admin", true)
	pPooled.SetListener(pooledLn)
	go pPooled.ListenAndServeTLS()

	time.Sleep(200 * time.Millisecond)

	results := Proxy(ProxyConfig{
		Config:     baseCfg(""),
		FreshAddr:  freshLn.Addr().String(),
		PooledAddr: pooledLn.Addr().String(),
	})
	// fresh-ssh, pooled-ssh = 2 modes
	assertResults(t, results, "proxy", 2)
}

func TestSSHBadAddr(t *testing.T) {
	// Point at a closed port — all iterations should fail gracefully
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cfg := baseCfg(addr)
	cfg.Iterations = 1
	cfg.Commands = 1
	results := SSH(cfg)
	for _, r := range results {
		if r.Errors == 0 && r.AvgMs > 0 {
			t.Errorf("%s/%s: expected errors connecting to closed port", r.Transport, r.Operation)
		}
	}
}

func TestHTTPSBadAddr(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	cfg := baseCfg(addr)
	cfg.Iterations = 1
	cfg.Commands = 1
	results := HTTPS(cfg)
	for _, r := range results {
		if r.Errors == 0 {
			t.Errorf("%s/%s: expected errors connecting to closed port", r.Transport, r.Operation)
		}
	}
}
