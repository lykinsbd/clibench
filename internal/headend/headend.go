// Package headend provides an SSH server that translates exec commands
// into HTTP requests to a remote site proxy. This is the "headend" half of
// the SSH-to-HTTP transparent WAN tunnel — it sits near the automation
// server and converts SSH to HTTP for efficient WAN transport.
package headend

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/quic-go/quic-go/http3"
	"golang.org/x/crypto/ssh"

	"github.com/lykinsbd/clibench/internal/sshutil"
)

// Server is an SSH server that forwards commands via HTTP to a site proxy.
type Server struct {
	addr       string
	backendURL string // e.g. "https://localhost:9443"
	user       string
	pass       string
	client     *http.Client
	cfg        *ssh.ServerConfig
	listener   net.Listener
}

// New creates a headend proxy. Transport is "https" or "http3".
func New(addr, backendURL, user, pass, transport string) (*Server, error) {
	cfg, err := sshutil.ServerConfig(user, pass)
	if err != nil {
		return nil, err
	}

	var rt http.RoundTripper
	if transport == "http3" {
		rt = &http3.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				NextProtos:         []string{http3.NextProtoH3},
			},
		}
	} else {
		rt = &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
	}

	return &Server{
		addr:       addr,
		backendURL: backendURL,
		user:       user,
		pass:       pass,
		cfg:        cfg,
		client:     &http.Client{Transport: rt, Timeout: 30 * time.Second},
	}, nil
}

// SetListener sets a custom net.Listener.
func (s *Server) SetListener(ln net.Listener) { s.listener = ln }

// Addr returns the listener's address.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Close stops the server.
// Close stops the server and releases the HTTP client.
func (s *Server) Close() error {
	if s.client != nil {
		s.client.CloseIdleConnections()
	}
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// ListenAndServe starts accepting SSH connections.
func (s *Server) ListenAndServe() error {
	var err error
	s.listener, err = sshutil.Listen(s.listener, s.addr, "Headend SSH")
	if err != nil {
		return err
	}
	return sshutil.Serve(s.listener, s.cfg, s.handleSession)
}

func (s *Server) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			req.Reply(false, nil)
			continue
		}
		var execCmd string
		if len(req.Payload) > 4 {
			execCmd = string(req.Payload[4:])
		}
		req.Reply(true, nil)
		if execCmd != "" {
			out, err := s.forward(execCmd)
			if err != nil {
				io.WriteString(ch, fmt.Sprintf("%%Error: %v\n", err))
			} else {
				io.WriteString(ch, out)
			}
		}
		ch.SendRequest("exit-status", false, []byte{0, 0, 0, 0})
		return
	}
}

func (s *Server) forward(payload string) (string, error) {
	lines := strings.Split(strings.TrimSpace(payload), "\n")
	if len(lines) == 1 {
		cmd := strings.ReplaceAll(strings.TrimSpace(lines[0]), " ", "+")
		url := fmt.Sprintf("%s/admin/exec/%s", s.backendURL, cmd)
		req, err := http.NewRequest("GET", url, nil)
		if err != nil {
			return "", err
		}
		req.SetBasicAuth(s.user, s.pass)
		resp, err := s.client.Do(req)
		if err != nil {
			return "", err
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		return string(body), nil
	}
	url := fmt.Sprintf("%s/admin/config", s.backendURL)
	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(s.user, s.pass)
	resp, err := s.client.Do(req)
	if err != nil {
		return "", err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return string(body), nil
}
