// Package proxy provides an HTTPS/HTTP3 server that forwards CLI commands
// to a backend network device over SSH. This is the "site proxy" pattern:
// automation talks HTTPS over the WAN, the proxy talks SSH over a
// low-latency campus link.
package proxy

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lykinsbd/clibench/internal/httphandler"
	"github.com/lykinsbd/clibench/internal/tlsutil"
	"golang.org/x/crypto/ssh"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
)

// Server is an HTTPS proxy that forwards commands to a backend SSH device.
type Server struct {
	addr        string
	backendAddr string
	user        string
	pass        string
	sshCfg      *ssh.ClientConfig
	pooled      bool
	mu          sync.Mutex
	pool        *ssh.Client
	listener    net.Listener
	packetConn  net.PacketConn
	srv         *http.Server
	h3srv       *http3.Server
}

// New creates a proxy server. If pooled is true, one SSH connection
// is reused across requests; otherwise each request gets a fresh one.
func New(addr, backendAddr, user, pass string, pooled bool) *Server {
	return &Server{
		addr:        addr,
		backendAddr: backendAddr,
		user:        user,
		pass:        pass,
		pooled:      pooled,
		sshCfg: &ssh.ClientConfig{
			User:            user,
			Auth:            []ssh.AuthMethod{ssh.Password(pass)},
			HostKeyCallback: ssh.InsecureIgnoreHostKey(),
			Timeout:         10 * time.Second,
		},
	}
}

// SetListener sets a custom net.Listener (e.g., with latency injection).
func (s *Server) SetListener(ln net.Listener) { s.listener = ln }

// Addr returns the listener's address, or "" if not yet listening.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Close stops the proxy server and releases all resources.
func (s *Server) Close() error {
	var firstErr error
	if s.h3srv != nil {
		if err := s.h3srv.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if s.srv != nil {
		if err := s.srv.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	s.mu.Lock()
	if s.pool != nil {
		s.pool.Close()
		s.pool = nil
	}
	s.mu.Unlock()
	return firstErr
}

// RunCommands implements httphandler.Runner by forwarding to SSH backend.
func (s *Server) RunCommands(cmds []string) (string, error) {
	if s.pooled {
		s.mu.Lock()
		defer s.mu.Unlock()
	}

	conn, pooled, err := s.getSSH()
	if err != nil {
		return "", fmt.Errorf("ssh dial: %w", err)
	}
	if !pooled {
		defer conn.Close()
	}

	var out strings.Builder
	for _, cmd := range cmds {
		sess, err := conn.NewSession()
		if err != nil {
			if pooled {
				s.resetPool()
			}
			return out.String(), fmt.Errorf("ssh session: %w", err)
		}
		b, err := sess.Output(cmd)
		sess.Close()
		if err != nil {
			if pooled {
				s.resetPool()
			}
			return out.String(), fmt.Errorf("ssh exec: %w", err)
		}
		out.Write(b)
	}
	return out.String(), nil
}

// ListenAndServeTLS starts the HTTPS proxy.
func (s *Server) ListenAndServeTLS() error {
	tlsCfg, err := tlsutil.SelfSignedConfig()
	if err != nil {
		return err
	}

	handler := httphandler.Mux(s.user, s.pass, s)

	if s.listener == nil {
		s.listener, err = net.Listen("tcp", s.addr)
		if err != nil {
			return err
		}
	}
	ln := tls.NewListener(s.listener, tlsCfg)
	s.srv = &http.Server{Handler: handler, TLSConfig: tlsCfg}
	log.Printf("Proxy HTTPS listening on %s → SSH backend %s (pooled=%v)", s.listener.Addr(), s.backendAddr, s.pooled)
	err = s.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// SetPacketConn sets a custom net.PacketConn for HTTP/3 serving.
func (s *Server) SetPacketConn(conn net.PacketConn) { s.packetConn = conn }

// ListenAndServeH3 starts the proxy as an HTTP/3 server.
func (s *Server) ListenAndServeH3() error {
	tlsCfg, err := tlsutil.SelfSignedConfig()
	if err != nil {
		return err
	}
	tlsCfg.NextProtos = []string{"h3"}

	handler := httphandler.Mux(s.user, s.pass, s)
	s.h3srv = &http3.Server{
		Handler:    handler,
		TLSConfig:  tlsCfg,
		QUICConfig: &quic.Config{Allow0RTT: true},
	}

	if s.packetConn == nil {
		s.packetConn, err = net.ListenPacket("udp", s.addr)
		if err != nil {
			return err
		}
	}
	log.Printf("Proxy HTTP/3 listening on %s → SSH backend %s (pooled=%v)", s.packetConn.LocalAddr(), s.backendAddr, s.pooled)
	err = s.h3srv.Serve(s.packetConn)
	if err != nil && err.Error() == "server closed" {
		return nil
	}
	return err
}

func (s *Server) getSSH() (*ssh.Client, bool, error) {
	if !s.pooled {
		c, err := ssh.Dial("tcp", s.backendAddr, s.sshCfg)
		return c, false, err
	}
	if s.pool != nil {
		return s.pool, true, nil
	}
	c, err := ssh.Dial("tcp", s.backendAddr, s.sshCfg)
	if err != nil {
		return nil, false, err
	}
	s.pool = c
	return c, true, nil
}

func (s *Server) resetPool() {
	if s.pool != nil {
		s.pool.Close()
		s.pool = nil
	}
}
