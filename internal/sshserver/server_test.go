package sshserver

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"golang.org/x/crypto/ssh"
)

func testDevice(t *testing.T) *device.Device {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "show_version.txt"), []byte("TestOS v1\n"), 0644)
	d, err := device.New("rtr1", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func startSSH(t *testing.T) string {
	t.Helper()
	dev := testDevice(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(ln.Addr().String(), dev)
	if err != nil {
		t.Fatal(err)
	}
	srv.SetListener(ln)
	go srv.ListenAndServe()
	time.Sleep(50 * time.Millisecond)
	return ln.Addr().String()
}

func sshClient(t *testing.T, addr string) *ssh.Client {
	t.Helper()
	cfg := &ssh.ClientConfig{
		User: "admin", Auth: []ssh.AuthMethod{ssh.Password("admin")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	}
	c, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestSSHExecMultiSession(t *testing.T) {
	addr := startSSH(t)
	conn := sshClient(t, addr)
	defer conn.Close()
	for i := 0; i < 5; i++ {
		sess, err := conn.NewSession()
		if err != nil {
			t.Fatal(err)
		}
		out, err := sess.Output("show version")
		sess.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), "TestOS v1") {
			t.Errorf("iteration %d: got %q", i, out)
		}
	}
}

func TestSSHExecBatchPayload(t *testing.T) {
	addr := startSSH(t)
	conn := sshClient(t, addr)
	defer conn.Close()
	sess, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.Output("show version\nshow version\nshow version")
	sess.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(out), "TestOS v1") != 3 {
		t.Errorf("expected 3 outputs, got %q", out)
	}
}

func TestSSHExecUnknownCommand(t *testing.T) {
	addr := startSSH(t)
	conn := sshClient(t, addr)
	defer conn.Close()
	sess, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	out, err := sess.Output("show bogus")
	sess.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(out), "%") {
		t.Errorf("expected %% error prefix, got %q", out)
	}
}

func TestSSHServerPortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	dev := testDevice(t)
	srv, err := New(addr, dev)
	if err != nil {
		t.Fatal(err)
	}
	// Don't set a listener — let it try to listen on the occupied port
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Error("expected error for port in use")
		}
	case <-time.After(2 * time.Second):
		t.Error("timed out waiting for port-in-use error")
	}
}
