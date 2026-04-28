// Package httpserver provides a TLS-enabled HTTP server that emulates
// the Cisco ASA HTTP interface for CLI automation.
//
// Endpoints:
//
//	GET  /admin/exec/show+version         — single command (URL-encoded)
//	GET  /admin/exec/cmd1/cmd2/cmd3       — multiple commands (slash-separated)
//	POST /admin/config                    — bulk commands (newline-delimited body)
package httpserver

import (
	"crypto/tls"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/tlsutil"
)

// Server is an HTTPS server backed by a Device.
type Server struct {
	dev      *device.Device
	addr     string
	listener net.Listener
	srv      *http.Server
}

// New creates an HTTPS server on addr backed by dev.
func New(addr string, dev *device.Device) *Server {
	return &Server{dev: dev, addr: addr}
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

// Close stops the server gracefully.
func (s *Server) Close() error {
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}

// ListenAndServeTLS starts the HTTPS listener with a self-signed cert.
// It returns nil when the server is closed via Close().
func (s *Server) ListenAndServeTLS() error {
	tlsCfg, err := tlsutil.SelfSignedConfig()
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/admin/exec/", s.handleExec)
	mux.HandleFunc("/admin/config", s.handleConfig)

	s.srv = &http.Server{
		Handler:   s.authMiddleware(mux),
		TLSConfig: tlsCfg,
	}

	if s.listener == nil {
		s.listener, err = net.Listen("tcp", s.addr)
		if err != nil {
			return err
		}
	}
	ln := tls.NewListener(s.listener, tlsCfg)
	log.Printf("HTTPS listening on %s", s.listener.Addr())
	err = s.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
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

// handleExec handles GET /admin/exec/show+version or /admin/exec/cmd1/cmd2
func (s *Server) handleExec(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/admin/exec/")
	if path == "" {
		http.Error(w, "no command", http.StatusBadRequest)
		return
	}

	// Split on "/" for multi-command support (ASA style)
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

// handleConfig handles POST /admin/config with newline-delimited commands.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

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


