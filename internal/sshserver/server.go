// Package sshserver provides a crypto/ssh listener that emulates
// a network device CLI session, following CiSSHGo patterns.
package sshserver

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"

	"github.com/lykinsbd/clibench/internal/device"
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
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}

	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == dev.Username && string(pass) == dev.Password {
				return nil, nil
			}
			return nil, fmt.Errorf("auth failed")
		},
	}
	cfg.AddHostKey(signer)

	return &Server{dev: dev, cfg: cfg, addr: addr}, nil
}

// SetListener sets a custom net.Listener (e.g., one wrapped with latency injection).
func (s *Server) SetListener(ln net.Listener) {
	s.listener = ln
}

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
// It returns nil when the listener is closed via Close().
func (s *Server) ListenAndServe() error {
	if s.listener == nil {
		ln, err := net.Listen("tcp", s.addr)
		if err != nil {
			return err
		}
		s.listener = ln
	}
	log.Printf("SSH listening on %s", s.listener.Addr())
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConn(conn)
	}
}

func (s *Server) handleConn(c net.Conn) {
	defer c.Close()
	sConn, chans, reqs, err := ssh.NewServerConn(c, s.cfg)
	if err != nil {
		return
	}
	defer sConn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			continue
		}
		go s.handleSession(ch, requests)
	}
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
				// Split newline-delimited payloads (batch exec mode)
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
	var line strings.Builder
	for {
		n, err := ch.Read(buf)
		if err != nil {
			return
		}
		for _, b := range buf[:n] {
			switch b {
			case '\r', '\n':
				io.WriteString(ch, "\r\n")
				cmd := strings.TrimSpace(line.String())
				line.Reset()
				if cmd == "exit" || cmd == "quit" {
					return
				}
				if cmd != "" {
					out := s.dev.Exec(cmd)
					io.WriteString(ch, out)
				}
				io.WriteString(ch, prompt)
			case 127, 8: // backspace
				if line.Len() > 0 {
					s := line.String()
					line.Reset()
					line.WriteString(s[:len(s)-1])
					io.WriteString(ch, "\b \b")
				}
			default:
				line.WriteByte(b)
				ch.Write([]byte{b})
			}
		}
	}
}
