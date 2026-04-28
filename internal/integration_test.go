package integration_test

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/httpserver"
	"github.com/lykinsbd/clibench/internal/sshserver"
	"golang.org/x/crypto/ssh"
)

func setupServers(t *testing.T) (sshAddr, httpsAddr string) {
	t.Helper()

	// Find transcripts relative to this test file
	transcriptsDir := "../transcripts"
	if _, err := os.Stat(transcriptsDir); err != nil {
		transcriptsDir = "transcripts"
	}
	dev, err := device.New("test-rtr", "admin", "admin", transcriptsDir)
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

func TestSSHExec(t *testing.T) {
	sshAddr, _ := setupServers(t)
	cfg := &ssh.ClientConfig{
		User:            "admin",
		Auth:            []ssh.AuthMethod{ssh.Password("admin")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, err := ssh.Dial("tcp", sshAddr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	sess, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.Output("show version")
	sess.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "test-rtr") {
		t.Errorf("expected hostname in output, got %q", string(out))
	}
}

func TestSSHBadAuth(t *testing.T) {
	sshAddr, _ := setupServers(t)
	cfg := &ssh.ClientConfig{
		User:            "admin",
		Auth:            []ssh.AuthMethod{ssh.Password("wrong")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	_, err := ssh.Dial("tcp", sshAddr, cfg)
	if err == nil {
		t.Error("expected auth failure")
	}
}

func TestHTTPSExec(t *testing.T) {
	_, httpsAddr := setupServers(t)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	url := fmt.Sprintf("https://%s/admin/exec/show+version", httpsAddr)
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("admin", "admin")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), "test-rtr") {
		t.Errorf("expected hostname in output, got %q", string(body))
	}
}

func TestHTTPSBadAuth(t *testing.T) {
	_, httpsAddr := setupServers(t)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	url := fmt.Sprintf("https://%s/admin/exec/show+version", httpsAddr)
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("admin", "wrong")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Errorf("expected 401, got %d", resp.StatusCode)
	}
}

func TestHTTPSMultiCmd(t *testing.T) {
	_, httpsAddr := setupServers(t)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	url := fmt.Sprintf("https://%s/admin/exec/show+version/show+ip+interface+brief", httpsAddr)
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("admin", "admin")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// Should contain output from both commands
	if !strings.Contains(string(body), "test-rtr") || !strings.Contains(string(body), "Interface") {
		t.Errorf("expected both command outputs, got %q", string(body))
	}
}

func TestHTTPSConfigPost(t *testing.T) {
	_, httpsAddr := setupServers(t)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	url := fmt.Sprintf("https://%s/admin/config", httpsAddr)
	body := "show version\nshow ip interface brief\n"
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.SetBasicAuth("admin", "admin")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("status %d", resp.StatusCode)
	}
	if !strings.Contains(string(out), "test-rtr") {
		t.Errorf("expected hostname, got %q", string(out))
	}
}
