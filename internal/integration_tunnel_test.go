package integration_test

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/headend"
	"github.com/lykinsbd/clibench/internal/proxy"
)

func setupTunnel(t *testing.T) (headendAddr, directSSHAddr string) {
	t.Helper()
	sshAddr, _ := setupServers(t)

	// Site proxy
	siteLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := proxy.New(siteLn.Addr().String(), sshAddr, "admin", "admin", true)
	p.SetListener(siteLn)
	go p.ListenAndServeTLS()

	// Headend proxy
	hLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	h, err := headend.New(hLn.Addr().String(), "https://"+siteLn.Addr().String(), "admin", "admin", "https")
	if err != nil {
		t.Fatal(err)
	}
	h.SetListener(hLn)
	go h.ListenAndServe()

	time.Sleep(300 * time.Millisecond)
	return hLn.Addr().String(), sshAddr
}

func TestTunnelOutputMatchesDirectSSH(t *testing.T) {
	hAddr, sshAddr := setupTunnel(t)
	direct := sshExec(t, sshAddr, "show version")
	tunnel := sshExec(t, hAddr, "show version")
	if direct != tunnel {
		t.Errorf("direct SSH %q != tunnel %q", direct, tunnel)
	}
}

func TestTunnelBatchOutputMatchesDirectSSH(t *testing.T) {
	hAddr, sshAddr := setupTunnel(t)
	payload := "show version\nshow ip interface brief\n"
	direct := sshExec(t, sshAddr, payload)
	tunnel := sshExec(t, hAddr, payload)
	if direct != tunnel {
		t.Errorf("direct SSH batch %q != tunnel batch %q", direct, tunnel)
	}
}

func TestTunnelMultipleCommands(t *testing.T) {
	hAddr, _ := setupTunnel(t)
	cmds := []string{"show version", "show ip interface brief"}
	for _, cmd := range cmds {
		out := sshExec(t, hAddr, cmd)
		if !strings.Contains(out, "test-rtr") && !strings.Contains(out, "Interface") {
			t.Errorf("cmd %q: unexpected output %q", cmd, out)
		}
	}
}
