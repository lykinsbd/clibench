package restconfserver_test

import (
	"crypto/tls"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/restconfserver"
)

func startTestServer(t *testing.T) (addr string, cleanup func()) {
	t.Helper()
	dev, err := device.New("test-rtr", "admin", "admin", "../../transcripts")
	if err != nil {
		t.Fatalf("device.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := restconfserver.New(ln.Addr().String(), dev)
	srv.SetListener(ln)
	go srv.ListenAndServeTLS()

	// Wait for server.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", ln.Addr().String(), 500*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ln.Addr().String(), func() { srv.Close() }
}

func testClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		Timeout: 5 * time.Second,
	}
}

func TestGetJSON(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	client := testClient()
	path := restconfserver.CommandToPath("show version")
	url := "https://" + addr + path

	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("admin", "admin")
	req.Header.Set("Accept", "application/yang-data+json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "cli:output") {
		t.Errorf("expected JSON envelope with cli:output: %s", body)
	}
	if !strings.Contains(string(body), "response") {
		t.Errorf("expected response field: %s", body)
	}
}

func TestGetXML(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	client := testClient()
	path := restconfserver.CommandToPath("show version")
	url := "https://" + addr + path

	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("admin", "admin")
	req.Header.Set("Accept", "application/yang-data+xml")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<output") {
		t.Errorf("expected XML output element: %s", body)
	}
	if !strings.Contains(string(body), "urn:cli") {
		t.Errorf("expected urn:cli namespace: %s", body)
	}
}

func TestPatchJSON(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	client := testClient()
	url := "https://" + addr + "/restconf/data/"

	body := "show version\nshow ip route\n"
	req, _ := http.NewRequest("PATCH", url, strings.NewReader(body))
	req.SetBasicAuth("admin", "admin")
	req.Header.Set("Content-Type", "application/yang-patch+json")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "ok") {
		t.Errorf("expected ok in patch response: %s", respBody)
	}
}

func TestUnauthorized(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	client := testClient()
	url := "https://" + addr + "/restconf/data/cli:show/version"

	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("wrong", "creds")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHostMeta(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	client := testClient()
	url := "https://" + addr + "/.well-known/host-meta"

	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("admin", "admin")

	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "restconf") {
		t.Errorf("expected restconf link: %s", body)
	}
}

func TestPathToCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"show version", "/restconf/data/cli:show/version"},
		{"show ip route", "/restconf/data/cli:show/ip/route"},
	}
	for _, tt := range tests {
		got := restconfserver.CommandToPath(tt.cmd)
		if got != tt.want {
			t.Errorf("CommandToPath(%q) = %q, want %q", tt.cmd, got, tt.want)
		}
	}
}
