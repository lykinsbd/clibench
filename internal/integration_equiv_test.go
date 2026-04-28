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

	"github.com/lykinsbd/clibench/internal/proxy"
	"golang.org/x/crypto/ssh"
)

func sshExec(t *testing.T, addr, cmd string) string {
	t.Helper()
	cfg := &ssh.ClientConfig{
		User: "admin", Auth: []ssh.AuthMethod{ssh.Password("admin")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	}
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sess, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.Output(cmd)
	sess.Close()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func httpsExec(t *testing.T, addr, cmd string) string {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
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

func httpsPost(t *testing.T, addr, body string) string {
	t.Helper()
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
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

func TestSSHAndHTTPSReturnIdenticalOutput(t *testing.T) {
	sshAddr, httpsAddr := setupServers(t)
	sshOut := sshExec(t, sshAddr, "show version")
	httpsOut := httpsExec(t, httpsAddr, "show version")
	if sshOut != httpsOut {
		t.Errorf("SSH output %q != HTTPS output %q", sshOut, httpsOut)
	}
}

func TestSSHAndHTTPSBatchIdenticalOutput(t *testing.T) {
	sshAddr, httpsAddr := setupServers(t)
	payload := "show version\nshow version\n"
	// SSH batch exec now splits on newlines, same as HTTPS POST
	cfg := &ssh.ClientConfig{
		User: "admin", Auth: []ssh.AuthMethod{ssh.Password("admin")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
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
	sshOut, err := sess.Output(payload)
	sess.Close()
	if err != nil {
		t.Fatal(err)
	}
	httpsOut := httpsPost(t, httpsAddr, payload)
	if string(sshOut) != httpsOut {
		t.Errorf("SSH batch %q != HTTPS batch %q", sshOut, httpsOut)
	}
}

func TestBackendEquivalence(t *testing.T) {
	sshAddr, httpsAddr := setupServers(t)
	cmds := []string{"show version", "show ip interface brief"}
	for _, cmd := range cmds {
		sshOut := sshExec(t, sshAddr, cmd)
		httpsGet := httpsExec(t, httpsAddr, cmd)
		httpsPost := httpsPost(t, httpsAddr, cmd+"\n")
		if sshOut != httpsGet {
			t.Errorf("cmd %q: SSH %q != HTTPS GET %q", cmd, sshOut, httpsGet)
		}
		if sshOut != httpsPost {
			t.Errorf("cmd %q: SSH %q != HTTPS POST %q", cmd, sshOut, httpsPost)
		}
	}
}

func TestProxyReturnsIdenticalOutput(t *testing.T) {
	sshAddr, httpsAddr := setupServers(t)
	// Start proxy pointing at the SSH backend
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := proxy.New(proxyLn.Addr().String(), sshAddr, "admin", "admin", false)
	p.SetListener(proxyLn)
	go p.ListenAndServeTLS()
	time.Sleep(100 * time.Millisecond)
	proxyAddr := proxyLn.Addr().String()

	sshOut := sshExec(t, sshAddr, "show version")
	httpsOut := httpsExec(t, httpsAddr, "show version")
	proxyOut := httpsExec(t, proxyAddr, "show version")
	if sshOut != httpsOut || sshOut != proxyOut {
		t.Errorf("outputs differ: ssh=%q https=%q proxy=%q", sshOut, httpsOut, proxyOut)
	}
}

func TestSSHMultipleSessionsOnOneConn(t *testing.T) {
	sshAddr, _ := setupServers(t)
	cfg := &ssh.ClientConfig{
		User: "admin", Auth: []ssh.AuthMethod{ssh.Password("admin")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	}
	conn, err := ssh.Dial("tcp", sshAddr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for i := 0; i < 10; i++ {
		sess, err := conn.NewSession()
		if err != nil {
			t.Fatalf("session %d: %v", i, err)
		}
		out, err := sess.Output("show version")
		sess.Close()
		if err != nil {
			t.Fatalf("session %d exec: %v", i, err)
		}
		if !strings.Contains(string(out), "test-rtr") {
			t.Errorf("session %d: unexpected output %q", i, out)
		}
	}
}

func TestHTTPSKeepAlive(t *testing.T) {
	_, httpsAddr := setupServers(t)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	for i := 0; i < 10; i++ {
		url := fmt.Sprintf("https://%s/admin/exec/show+version", httpsAddr)
		req, _ := http.NewRequest("GET", url, nil)
		req.SetBasicAuth("admin", "admin")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("iter %d: status %d", i, resp.StatusCode)
		}
	}
}

func TestHTTPSFreshConnDisableKeepAlive(t *testing.T) {
	_, httpsAddr := setupServers(t)
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: true},
			DisableKeepAlives: true,
		},
		Timeout: 5 * time.Second,
	}
	for i := 0; i < 5; i++ {
		url := fmt.Sprintf("https://%s/admin/exec/show+version", httpsAddr)
		req, _ := http.NewRequest("GET", url, nil)
		req.SetBasicAuth("admin", "admin")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Errorf("iter %d: status %d", i, resp.StatusCode)
		}
	}
}

func TestConcurrentSSHSessions(t *testing.T) {
	sshAddr, _ := setupServers(t)
	cfg := &ssh.ClientConfig{
		User: "admin", Auth: []ssh.AuthMethod{ssh.Password("admin")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	}
	conn, err := ssh.Dial("tcp", sshAddr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sess, err := conn.NewSession()
			if err != nil {
				errs <- err
				return
			}
			_, err = sess.Output("show version")
			sess.Close()
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("concurrent session error: %v", err)
	}
}

func TestConcurrentHTTPSRequests(t *testing.T) {
	_, httpsAddr := setupServers(t)
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			url := fmt.Sprintf("https://%s/admin/exec/show+version", httpsAddr)
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
		t.Errorf("concurrent HTTPS error: %v", err)
	}
}

func TestProxyPooledConcurrent(t *testing.T) {
	sshAddr, _ := setupServers(t)
	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := proxy.New(proxyLn.Addr().String(), sshAddr, "admin", "admin", true)
	p.SetListener(proxyLn)
	go p.ListenAndServeTLS()
	time.Sleep(100 * time.Millisecond)
	proxyAddr := proxyLn.Addr().String()

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	var wg sync.WaitGroup
	errs := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			url := fmt.Sprintf("https://%s/admin/exec/show+version", proxyAddr)
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
		t.Errorf("concurrent proxy error: %v", err)
	}
}
