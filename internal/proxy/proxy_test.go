package proxy

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/sshserver"
)

// tcpProxy forwards connections and can kill them all on Close.
type tcpProxy struct {
	ln    net.Listener
	conns []net.Conn
	mu    sync.Mutex
}

func newTCPProxy(t *testing.T, backend string) *tcpProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &tcpProxy{ln: ln}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b, err := net.Dial("tcp", backend)
			if err != nil {
				c.Close()
				continue
			}
			p.mu.Lock()
			p.conns = append(p.conns, c, b)
			p.mu.Unlock()
			go io.Copy(b, c)
			go io.Copy(c, b)
		}
	}()
	return p
}

func (p *tcpProxy) Addr() string { return p.ln.Addr().String() }

func (p *tcpProxy) Close() {
	p.ln.Close()
	p.mu.Lock()
	for _, c := range p.conns {
		c.Close()
	}
	p.conns = nil
	p.mu.Unlock()
}

func startBackend(t *testing.T) (addr string, cleanup func()) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "show_version.txt"), []byte("TestOS v1\n"), 0644)
	dev, err := device.New("rtr1", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := sshserver.New(ln.Addr().String(), dev)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetListener(ln)
	go srv.ListenAndServe()
	time.Sleep(50 * time.Millisecond)
	return ln.Addr().String(), func() { ln.Close() }
}

func startProxy(t *testing.T, backendAddr string, pooled bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := New(ln.Addr().String(), backendAddr, "admin", "admin", pooled)
	p.SetListener(ln)
	go p.ListenAndServeTLS()
	time.Sleep(100 * time.Millisecond)
	return ln.Addr().String()
}

func proxyClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
}

func doExec(t *testing.T, addr, user, pass string) (int, string) {
	t.Helper()
	url := fmt.Sprintf("https://%s/admin/exec/show+version", addr)
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth(user, pass)
	resp, err := proxyClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp.StatusCode, string(b)
}

func TestProxyFreshExec(t *testing.T) {
	backend, cleanup := startBackend(t)
	defer cleanup()
	addr := startProxy(t, backend, false)
	code, body := doExec(t, addr, "admin", "admin")
	if code != 200 {
		t.Errorf("status = %d", code)
	}
	if !strings.Contains(body, "TestOS v1") {
		t.Errorf("got %q", body)
	}
}

func TestProxyPooledExec(t *testing.T) {
	backend, cleanup := startBackend(t)
	defer cleanup()
	addr := startProxy(t, backend, true)
	for i := 0; i < 3; i++ {
		code, body := doExec(t, addr, "admin", "admin")
		if code != 200 {
			t.Errorf("iter %d: status = %d", i, code)
		}
		if !strings.Contains(body, "TestOS v1") {
			t.Errorf("iter %d: got %q", i, body)
		}
	}
}

func TestProxyPooledResetOnError(t *testing.T) {
	backend, _ := startBackend(t)
	tp := newTCPProxy(t, backend)
	addr := startProxy(t, tp.Addr(), true)
	// Warm up the pool
	code, _ := doExec(t, addr, "admin", "admin")
	if code != 200 {
		t.Fatalf("warmup failed: %d", code)
	}
	// Kill all TCP connections through the proxy
	tp.Close()
	time.Sleep(100 * time.Millisecond)
	// Next request should fail (pooled connection is dead)
	code, _ = doExec(t, addr, "admin", "admin")
	if code != 502 {
		t.Errorf("expected 502 after backend killed, got %d", code)
	}
}

func TestProxyBadAuth(t *testing.T) {
	backend, cleanup := startBackend(t)
	defer cleanup()
	addr := startProxy(t, backend, false)
	code, _ := doExec(t, addr, "admin", "wrong")
	if code != 401 {
		t.Errorf("status = %d, want 401", code)
	}
}

func TestProxyBackendDown(t *testing.T) {
	// Point proxy at a port with nothing listening
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	deadAddr := ln.Addr().String()
	ln.Close()
	addr := startProxy(t, deadAddr, false)
	code, _ := doExec(t, addr, "admin", "admin")
	if code != 502 {
		t.Errorf("status = %d, want 502", code)
	}
}

func TestProxyConfigPost(t *testing.T) {
	backend, cleanup := startBackend(t)
	defer cleanup()
	addr := startProxy(t, backend, false)
	url := fmt.Sprintf("https://%s/admin/config", addr)
	req, _ := http.NewRequest("POST", url, strings.NewReader("show version\nshow version\n"))
	req.SetBasicAuth("admin", "admin")
	resp, err := proxyClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status = %d", resp.StatusCode)
	}
	// Should contain output from both commands
	if strings.Count(string(b), "TestOS v1") != 2 {
		t.Errorf("expected 2 outputs, got %q", b)
	}
}

func TestProxyBackendDiesMidRequest(t *testing.T) {
	backend, _ := startBackend(t)
	tp := newTCPProxy(t, backend)
	addr := startProxy(t, tp.Addr(), true)
	// Warm up pool
	code, _ := doExec(t, addr, "admin", "admin")
	if code != 200 {
		t.Fatalf("warmup: %d", code)
	}
	// Kill all connections
	tp.Close()
	time.Sleep(100 * time.Millisecond)
	code, _ = doExec(t, addr, "admin", "admin")
	if code != 502 {
		t.Errorf("expected 502, got %d", code)
	}
}
