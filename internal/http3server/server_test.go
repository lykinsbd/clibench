package http3server

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

	"github.com/quic-go/quic-go/http3"

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

func startHTTP3(t *testing.T) string {
	t.Helper()
	dev := testDevice(t)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := New(conn.LocalAddr().String(), dev)
	srv.SetPacketConn(conn)
	go srv.ListenAndServe()
	time.Sleep(200 * time.Millisecond)
	return conn.LocalAddr().String()
}

func h3Client() *http.Client {
	return &http.Client{
		Transport: &http3.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				NextProtos:         []string{http3.NextProtoH3},
			},
		},
		Timeout: 5 * time.Second,
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
	resp, err := h3Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestHTTP3ExecEmptyPath(t *testing.T) {
	addr := startHTTP3(t)
	resp, _ := doReq(t, "GET", fmt.Sprintf("https://%s/admin/exec/", addr), "", "admin", "admin")
	if resp.StatusCode != 400 {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestHTTP3ConfigGetMethod(t *testing.T) {
	addr := startHTTP3(t)
	resp, _ := doReq(t, "GET", fmt.Sprintf("https://%s/admin/config", addr), "", "admin", "admin")
	if resp.StatusCode != 405 {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}

func TestHTTP3ConfigEmptyBody(t *testing.T) {
	addr := startHTTP3(t)
	resp, body := doReq(t, "POST", fmt.Sprintf("https://%s/admin/config", addr), "", "admin", "admin")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if strings.TrimSpace(body) != "" {
		t.Errorf("expected empty output, got %q", body)
	}
}

func TestHTTP3ExecURLDecoding(t *testing.T) {
	addr := startHTTP3(t)
	resp, body := doReq(t, "GET", fmt.Sprintf("https://%s/admin/exec/show+ip+interface+brief", addr), "", "admin", "admin")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "10.0.0.1") {
		t.Errorf("expected decoded command output, got %q", body)
	}
}

func TestHTTP3BadAuth(t *testing.T) {
	addr := startHTTP3(t)
	resp, _ := doReq(t, "GET", fmt.Sprintf("https://%s/admin/exec/show+version", addr), "", "admin", "wrong")
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHTTP3ExecShowVersion(t *testing.T) {
	addr := startHTTP3(t)
	resp, body := doReq(t, "GET", fmt.Sprintf("https://%s/admin/exec/show+version", addr), "", "admin", "admin")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "TestOS") {
		t.Errorf("expected version output, got %q", body)
	}
}

func TestHTTP3ConfigPost(t *testing.T) {
	addr := startHTTP3(t)
	resp, body := doReq(t, "POST", fmt.Sprintf("https://%s/admin/config", addr), "show version\nshow ip interface brief\n", "admin", "admin")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "TestOS") || !strings.Contains(body, "10.0.0.1") {
		t.Errorf("expected both command outputs, got %q", body)
	}
}

func TestHTTP3MultiCmd(t *testing.T) {
	addr := startHTTP3(t)
	resp, body := doReq(t, "GET", fmt.Sprintf("https://%s/admin/exec/show+version/show+ip+interface+brief", addr), "", "admin", "admin")
	if resp.StatusCode != 200 {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
	if !strings.Contains(body, "TestOS") || !strings.Contains(body, "10.0.0.1") {
		t.Errorf("expected both command outputs, got %q", body)
	}
}
