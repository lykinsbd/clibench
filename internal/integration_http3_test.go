package integration_test

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/http3server"
)

func startHTTP3Server(t *testing.T) string {
	t.Helper()
	transcriptsDir := "../transcripts"
	dev, err := device.New("test-rtr", "admin", "admin", transcriptsDir)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := http3server.New(conn.LocalAddr().String(), dev)
	srv.SetPacketConn(conn)
	go srv.ListenAndServe()
	time.Sleep(200 * time.Millisecond)
	return conn.LocalAddr().String()
}

func http3Exec(t *testing.T, addr, cmd string) string {
	t.Helper()
	client := &http.Client{
		Transport: &http3.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				NextProtos:         []string{http3.NextProtoH3},
			},
		},
		Timeout: 5 * time.Second,
	}
	encoded := strings.ReplaceAll(cmd, " ", "+")
	url := fmt.Sprintf("https://%s/admin/exec/%s", addr, encoded)
	req, _ := http.NewRequest("GET", url, nil)
	req.SetBasicAuth("admin", "admin")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

func http3Post(t *testing.T, addr, body string) string {
	t.Helper()
	client := &http.Client{
		Transport: &http3.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				NextProtos:         []string{http3.NextProtoH3},
			},
		},
		Timeout: 5 * time.Second,
	}
	url := fmt.Sprintf("https://%s/admin/config", addr)
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.SetBasicAuth("admin", "admin")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(b)
}

func TestHTTP3AndHTTPSReturnIdenticalOutput(t *testing.T) {
	_, httpsAddr := setupServers(t)
	h3Addr := startHTTP3Server(t)

	httpsOut := httpsExec(t, httpsAddr, "show version")
	h3Out := http3Exec(t, h3Addr, "show version")
	if httpsOut != h3Out {
		t.Errorf("HTTPS output %q != HTTP/3 output %q", httpsOut, h3Out)
	}
}

func TestHTTP3AndSSHReturnIdenticalOutput(t *testing.T) {
	sshAddr, _ := setupServers(t)
	h3Addr := startHTTP3Server(t)

	sshOut := sshExec(t, sshAddr, "show version")
	h3Out := http3Exec(t, h3Addr, "show version")
	if sshOut != h3Out {
		t.Errorf("SSH output %q != HTTP/3 output %q", sshOut, h3Out)
	}
}

func TestHTTP3BatchIdenticalToHTTPS(t *testing.T) {
	_, httpsAddr := setupServers(t)
	h3Addr := startHTTP3Server(t)

	payload := "show version\nshow version\n"
	httpsOut := httpsPost(t, httpsAddr, payload)
	h3Out := http3Post(t, h3Addr, payload)
	if httpsOut != h3Out {
		t.Errorf("HTTPS batch %q != HTTP/3 batch %q", httpsOut, h3Out)
	}
}

func TestHTTP3BackendEquivalence(t *testing.T) {
	sshAddr, httpsAddr := setupServers(t)
	h3Addr := startHTTP3Server(t)

	cmds := []string{"show version", "show ip interface brief"}
	for _, cmd := range cmds {
		sshOut := sshExec(t, sshAddr, cmd)
		httpsOut := httpsExec(t, httpsAddr, cmd)
		h3Out := http3Exec(t, h3Addr, cmd)
		if sshOut != h3Out {
			t.Errorf("cmd %q: SSH %q != HTTP/3 %q", cmd, sshOut, h3Out)
		}
		if httpsOut != h3Out {
			t.Errorf("cmd %q: HTTPS %q != HTTP/3 %q", cmd, httpsOut, h3Out)
		}
	}
}

func TestConcurrentHTTP3Requests(t *testing.T) {
	h3Addr := startHTTP3Server(t)
	client := &http.Client{
		Transport: &http3.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				NextProtos:         []string{http3.NextProtoH3},
			},
		},
		Timeout: 5 * time.Second,
	}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			url := fmt.Sprintf("https://%s/admin/exec/show+version", h3Addr)
			req, _ := http.NewRequest("GET", url, nil)
			req.SetBasicAuth("admin", "admin")
			resp, err := client.Do(req)
			if err != nil {
				errs <- err
				return
			}
			io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 200 {
				errs <- fmt.Errorf("status %d", resp.StatusCode)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent HTTP/3 error: %v", err)
	}
}
