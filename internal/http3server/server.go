// Package http3server provides an HTTP/3 (QUIC) server that reuses the
// httpserver handler logic. Same ASA-style endpoints, different transport.
package http3server

import (
	"io"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/tlsutil"
)

// Server is an HTTP/3 server backed by a Device.
type Server struct {
	dev  *device.Device
	addr string
	conn net.PacketConn
	srv  *http3.Server
}

// New creates an HTTP/3 server on addr backed by dev.
func New(addr string, dev *device.Device) *Server {
	return &Server{dev: dev, addr: addr}
}

// SetPacketConn sets a custom net.PacketConn for the server.
func (s *Server) SetPacketConn(conn net.PacketConn) {
	s.conn = conn
}

// Addr returns the listener's address, or "" if not yet listening.
func (s *Server) Addr() string {
	if s.conn != nil {
		return s.conn.LocalAddr().String()
	}
	return ""
}

// Close stops the server.
func (s *Server) Close() error {
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}

// ListenAndServe starts the HTTP/3 listener with a self-signed cert.
func (s *Server) ListenAndServe() error {
	tlsCfg, err := tlsutil.SelfSignedConfig()
	if err != nil {
		return err
	}
	tlsCfg.NextProtos = []string{http3.NextProtoH3}

	mux := http.NewServeMux()
	mux.HandleFunc("/admin/exec/", s.handleExec)
	mux.HandleFunc("/admin/config", s.handleConfig)

	s.srv = &http3.Server{
		Handler:   s.authMiddleware(mux),
		TLSConfig: tlsCfg,
		QUICConfig: &quic.Config{
			Allow0RTT: true,
		},
	}

	if s.conn == nil {
		s.conn, err = net.ListenPacket("udp", s.addr)
		if err != nil {
			return err
		}
	}
	log.Printf("HTTP/3 listening on %s", s.conn.LocalAddr())
	return s.srv.Serve(s.conn)
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != s.dev.Username || pass != s.dev.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="device"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/exec/")
	if path == "" {
		http.Error(w, "no command", http.StatusBadRequest)
		return
	}
	parts := strings.Split(path, "/")
	var out strings.Builder
	for _, p := range parts {
		cmd := strings.ReplaceAll(p, "+", " ")
		cmd = strings.TrimSpace(cmd)
		if cmd == "" {
			continue
		}
		out.WriteString(s.dev.Exec(cmd))
	}
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, out.String())
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	body := make([]byte, 0, 1024)
	buf := make([]byte, 1024)
	for {
		n, err := r.Body.Read(buf)
		body = append(body, buf[:n]...)
		if err != nil {
			break
		}
	}
	r.Body.Close()

	var out strings.Builder
	for _, line := range strings.Split(string(body), "\n") {
		cmd := strings.TrimSpace(line)
		if cmd == "" {
			continue
		}
		out.WriteString(s.dev.Exec(cmd))
	}
	w.Header().Set("Content-Type", "text/plain")
	io.WriteString(w, out.String())
}
