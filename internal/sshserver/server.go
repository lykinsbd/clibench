// Package sshserver provides a crypto/ssh listener that emulates
// a network device CLI session, following CiSSHGo patterns.
package sshserver

import (
	"io"
	"net"
	"strings"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/sshutil"
	"golang.org/x/crypto/ssh"
)

// Server is an SSH server backed by a Device.
type Server struct {
	dev      *device.Device
	cfg      *ssh.ServerConfig
	addr     string
	listener net.Listener
}

// New creates an SSH server on addr backed by dev.
func New(addr string, dev *device.Device) (*Server, error) {
	cfg, err := sshutil.ServerConfig(dev.Username, dev.Password)
	if err != nil {
		return nil, err
	}
	return &Server{dev: dev, cfg: cfg, addr: addr}, nil
}

// SetListener sets a custom net.Listener (e.g., one wrapped with latency injection).
func (s *Server) SetListener(ln net.Listener) { s.listener = ln }

// Addr returns the listener's address, or "" if not yet listening.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Close stops the server by closing the listener.
func (s *Server) Close() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// ListenAndServe starts accepting SSH connections.
func (s *Server) ListenAndServe() error {
	var err error
	s.listener, err = sshutil.Listen(s.listener, s.addr, "SSH")
	if err != nil {
		return err
	}
	return sshutil.Serve(s.listener, s.cfg, s.handleSession)
}

func (s *Server) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()

	var execCmd string
	for req := range reqs {
		switch req.Type {
		case "pty-req", "window-change":
			req.Reply(true, nil)
		case "shell":
			req.Reply(true, nil)
			s.interactiveSession(ch)
			return
		case "exec":
			if len(req.Payload) > 4 {
				execCmd = string(req.Payload[4:])
			}
			req.Reply(true, nil)
			if execCmd != "" {
				var out strings.Builder
				for _, line := range strings.Split(execCmd, "\n") {
					cmd := strings.TrimSpace(line)
					if cmd != "" {
						out.WriteString(s.dev.Exec(cmd))
					}
				}
				io.WriteString(ch, out.String())
			}
			ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
			return
		default:
			req.Reply(false, nil)
		}
	}
}

func (s *Server) interactiveSession(ch ssh.Channel) {
	prompt := s.dev.Hostname + "#"
	io.WriteString(ch, prompt)

	buf := make([]byte, 4096)
	var line []byte
	for {
		n, err := ch.Read(buf)
		if err != nil {
			return
		}
		for _, b := range buf[:n] {
			switch b {
			case '\r', '\n':
				io.WriteString(ch, "\r\n")
				cmd := strings.TrimSpace(string(line))
				line = line[:0]
				if cmd == "exit" || cmd == "quit" {
					return
				}
				if cmd != "" {
					out := s.dev.Exec(cmd)
					io.WriteString(ch, out)
				}
				io.WriteString(ch, prompt)
			case 127, 8: // backspace
				if len(line) > 0 {
					line = line[:len(line)-1]
					io.WriteString(ch, "\b \b")
				}
			default:
				line = append(line, b)
				ch.Write([]byte{b})
			}
		}
	}
}
