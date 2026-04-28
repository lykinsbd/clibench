package integration_test

import (
	"io"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestSSHPTYShellExec(t *testing.T) {
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

	sess, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	if err := sess.RequestPty("vt100", 1000, 511, ssh.TerminalModes{}); err != nil {
		t.Fatal(err)
	}
	w, err := sess.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	r, err := sess.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := sess.Shell(); err != nil {
		t.Fatal(err)
	}

	prompt := "test-rtr#"
	readUntil := func(prompt string) string {
		buf := make([]byte, 4096)
		var acc string
		for {
			n, err := r.Read(buf)
			if n > 0 {
				acc += string(buf[:n])
				if strings.Contains(acc, prompt) {
					return acc
				}
			}
			if err != nil {
				t.Fatalf("read error: %v (got so far: %q)", err, acc)
			}
		}
	}

	// Wait for initial prompt
	readUntil(prompt)

	// Send a command and read output
	io.WriteString(w, "show version\n")
	out := readUntil(prompt)
	if !strings.Contains(out, "test-rtr") {
		t.Errorf("expected hostname in PTY output, got %q", out)
	}

	// Send exit
	io.WriteString(w, "exit\n")
}

func TestSSHPTYAndExecReturnSameContent(t *testing.T) {
	sshAddr, _ := setupServers(t)
	cfg := &ssh.ClientConfig{
		User: "admin", Auth: []ssh.AuthMethod{ssh.Password("admin")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), Timeout: 5 * time.Second,
	}

	// Get exec output
	execOut := sshExec(t, sshAddr, "show version")

	// Get PTY output
	conn, err := ssh.Dial("tcp", sshAddr, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	sess, err := conn.NewSession()
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	sess.RequestPty("vt100", 1000, 511, ssh.TerminalModes{})
	w, _ := sess.StdinPipe()
	r, _ := sess.StdoutPipe()
	sess.Shell()

	prompt := "test-rtr#"
	readUntil := func() string {
		buf := make([]byte, 4096)
		var acc string
		for {
			n, err := r.Read(buf)
			if n > 0 {
				acc += string(buf[:n])
				if strings.Contains(acc, prompt) {
					return acc
				}
			}
			if err != nil {
				t.Fatalf("read: %v", err)
			}
		}
	}

	readUntil() // initial prompt
	io.WriteString(w, "show version\n")
	ptyRaw := readUntil()
	io.WriteString(w, "exit\n")

	// PTY output includes echo + prompt; extract just the command output
	// Strip the echoed command and trailing prompt
	ptyOut := ptyRaw
	if idx := strings.Index(ptyOut, "\n"); idx >= 0 {
		ptyOut = ptyOut[idx+1:] // skip echoed command line
	}
	if idx := strings.LastIndex(ptyOut, prompt); idx >= 0 {
		ptyOut = ptyOut[:idx]
	}
	// Normalize \r\n to \n
	ptyOut = strings.ReplaceAll(ptyOut, "\r\n", "\n")

	if ptyOut != execOut {
		t.Errorf("PTY output differs from exec output\nPTY:  %q\nExec: %q", ptyOut, execOut)
	}
}
