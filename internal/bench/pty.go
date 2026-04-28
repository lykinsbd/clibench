package bench

import (
	"fmt"
	"io"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ptyReader wraps an io.Reader with a buffer so readUntil doesn't lose
// data that arrives after the match in the same Read() call.
type ptyReader struct {
	r       io.Reader
	buf     string
	timeout time.Duration
}

func (p *ptyReader) readUntil(target string) error {
	b := make([]byte, 4096)
	deadline := time.NewTimer(p.timeout)
	defer deadline.Stop()

	type readResult struct {
		n   int
		err error
	}

	for {
		if idx := strings.Index(p.buf, target); idx >= 0 {
			p.buf = p.buf[idx+len(target):]
			return nil
		}

		ch := make(chan readResult, 1)
		go func() {
			n, err := p.r.Read(b)
			ch <- readResult{n, err}
		}()

		select {
		case res := <-ch:
			if res.n > 0 {
				p.buf += string(b[:res.n])
			}
			if res.err != nil {
				return res.err
			}
		case <-deadline.C:
			return fmt.Errorf("timeout waiting for %q (buf: %q)", target, p.buf)
		}
	}
}

// ptyExecCmds runs commands over a PTY shell session with realistic
// session preparation and per-command echo verification, matching the
// protocol-level behavior of Netmiko, Ansible network_cli, and Scrapli.
func ptyExecCmds(sess *ssh.Session, prompt string, cmds int) error {
	w, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	r, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	if err := sess.RequestPty("vt100", 1000, 511, ssh.TerminalModes{}); err != nil {
		return err
	}
	if err := sess.Shell(); err != nil {
		return err
	}

	pr := &ptyReader{r: r, timeout: 10 * time.Second}

	// Wait for initial prompt
	if err := pr.readUntil(prompt); err != nil {
		return err
	}

	// Session preparation: disable paging and set terminal width.
	for _, prepCmd := range []string{"terminal length 0", "terminal width 511"} {
		fmt.Fprintf(w, "%s\n", prepCmd)
		if err := pr.readUntil(prepCmd); err != nil {
			return err
		}
		if err := pr.readUntil(prompt); err != nil {
			return err
		}
	}

	// Execute commands with echo verification per command
	for i := 0; i < cmds; i++ {
		cmd := "show version"
		fmt.Fprintf(w, "%s\n", cmd)
		if err := pr.readUntil(cmd); err != nil {
			return err
		}
		if err := pr.readUntil(prompt); err != nil {
			return err
		}
	}

	fmt.Fprintf(w, "exit\n")
	return nil
}
