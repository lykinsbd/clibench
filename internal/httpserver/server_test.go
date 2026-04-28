package httpserver

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
)

func testDevice(t *testing.T) *device.Device {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "show_version.txt"), []byte("TestOS v1\n"), 0644)
	os.WriteFile(filepath.Join(dir, "show_ip_interface_brief.txt"), []byte("Gi0/0 10.0.0.1\n"), 0644)
	d, err := device.New("rtr1", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func startHTTPS(t *testing.T) string {
	t.Helper()
	dev := testDevice(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(ln.Addr().String(), dev)
	srv.SetListener(ln)
	go srv.ListenAndServeTLS()
	time.Sleep(100 * time.Millisecond)
	return ln.Addr().String()
}

func httpsClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
}

func doReq(t *testing.T, method, url, body, user, pass string) (*http.Response, string) {
	t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth(user, pass)
	resp, err := httpsClient().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestHTTPSExecEmptyPath(t *testing.T) {
	addr := startHTTPS(t)
	resp, _ := doReq(t, "GET", fmt.Sprintf("https://%s/admin/exec/", addr), "", "admin", "admin")
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTPSConfigGetMethod(t *testing.T) {
	addr := startHTTPS(t)
	resp, _ := doReq(t, "GET", fmt.Sprintf("https://%s/admin/config", addr), "", "admin", "admin")
	if resp.StatusCode != 405 {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestHTTPSConfigEmptyBody(t *testing.T) {
	addr := startHTTPS(t)
	resp, body := doReq(t, "POST", fmt.Sprintf("https://%s/admin/config", addr), "", "admin", "admin")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("expected empty output, got %q", body)
	}
}

func TestHTTPSExecURLDecoding(t *testing.T) {
	addr := startHTTPS(t)
	resp, body := doReq(t, "GET", fmt.Sprintf("https://%s/admin/exec/show+ip+interface+brief", addr), "", "admin", "admin")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "10.0.0.1") {
		t.Errorf("expected decoded command output, got %q", body)
	}
}

func TestHTTPSServerPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	dev := testDevice(t)
	srv := New(addr, dev)
	// Don't set listener — let it try to listen on the occupied port
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServeTLS() }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected error for port in use")
		}
	case <-time.After(2 * time.Second):
		t.Error("timed out waiting for port-in-use error")
	}
}
