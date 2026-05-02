package headend

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/proxy"
	"github.com/lykinsbd/clibench/internal/sshserver"
	"golang.org/x/crypto/ssh"
)

func setup(t *testing.T) (headendAddr, directSSHAddr string) {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "show_version.txt"), []byte("TestOS v1\n"), 0644)
	os.WriteFile(filepath.Join(dir, "show_ip_interface_brief.txt"), []byte("Gi0/0 10.0.0.1\n"), 0644)
	dev, err := device.New("test-rtr", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}

	// Backend SSH (device)
	sshLn, _ := net.Listen("tcp", "127.0.0.1:0")
	sshSrv, _ := sshserver.New(sshLn.Addr().String(), dev)
	sshSrv.SetListener(sshLn)
	go sshSrv.ListenAndServe()

	// Site proxy (HTTPS → SSH)
	proxyLn, _ := net.Listen("tcp", "127.0.0.1:0")
	p := proxy.New(proxyLn.Addr().String(), sshLn.Addr().String(), "admin", "admin", true)
	p.SetListener(proxyLn)
	go p.ListenAndServeTLS()

	// Headend proxy (SSH → HTTPS)
	hLn, _ := net.Listen("tcp", "127.0.0.1:0")
	h, err := New(hLn.Addr().String(), "https://"+proxyLn.Addr().String(), "admin", "admin", "https")
	if err != nil {
		t.Fatal(err)
	}
	h.SetListener(hLn)
	go h.ListenAndServe()

	time.Sleep(300 * time.Millisecond)
	return hLn.Addr().String(), sshLn.Addr().String()
}

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

func TestHeadendSingleCommand(t *testing.T) {
	hAddr, _ := setup(t)
	out := sshExec(t, hAddr, "show version")
	if !strings.Contains(out, "TestOS") {
		t.Errorf("expected version output, got %q", out)
	}
}

func TestHeadendBatchCommand(t *testing.T) {
	hAddr, _ := setup(t)
	out := sshExec(t, hAddr, "show version\nshow ip interface brief\n")
	if !strings.Contains(out, "TestOS") || !strings.Contains(out, "10.0.0.1") {
		t.Errorf("expected both outputs, got %q", out)
	}
}

func TestHeadendOutputMatchesDirect(t *testing.T) {
	hAddr, directAddr := setup(t)
	direct := sshExec(t, directAddr, "show version")
	tunnel := sshExec(t, hAddr, "show version")
	if direct != tunnel {
		t.Errorf("direct %q != tunnel %q", direct, tunnel)
	}
}

func TestHeadendBadAuth(t *testing.T) {
	hAddr, _ := setup(t)
	cfg := &ssh.ClientConfig{
		User: "admin", Auth: []ssh.AuthMethod{ssh.Password("wrong")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	}
	_, err := ssh.Dial("tcp", hAddr, cfg)
	if err == nil {
		t.Error("expected auth failure")
	}
}
