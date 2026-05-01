// Package headend provides an SSH server that translates exec commands
// into HTTP requests to a remote site proxy. This is the "headend" half of
// the SSH-to-HTTP transparent WAN tunnel — it sits near the automation
// server and converts SSH to HTTP for efficient WAN transport.
package headend

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/quic-go/quic-go/http3"
	"golang.org/x/crypto/ssh"
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

// New creates an edge proxy. Transport is "https" or "http3".
func New(addr, backendURL, user, pass, transport string) (*Server, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}

	sshCfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(p) == pass {
				return nil, nil
			}
			return nil, fmt.Errorf("auth failed")
		},
	}
	sshCfg.AddHostKey(signer)

	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	var rt http.RoundTripper
	if transport == "http3" {
		rt = &http3.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				NextProtos:         []string{http3.NextProtoH3},
			},
		}
	} else {
		rt = &http.Transport{TLSClientConfig: tlsCfg}
	}

	return &Server{
		addr:       addr,
		backendURL: backendURL,
		user:       user,
		pass:       pass,
		cfg:        sshCfg,
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
func (s *Server) Close() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// ListenAndServe starts accepting SSH connections.
func (s *Server) ListenAndServe() error {
	if s.listener == nil {
		ln, err := net.Listen("tcp", s.addr)
		if err != nil {
			return err
		}
		s.listener = ln
	}
	log.Printf("Edge proxy SSH on %s → %s", s.listener.Addr(), s.backendURL)
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
		// Single command → GET /admin/exec/
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
	// Multi-line → POST /admin/config
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
