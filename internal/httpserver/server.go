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
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/httphandler"
	"github.com/lykinsbd/clibench/internal/tlsutil"
)

// deviceRunner adapts *device.Device to httphandler.Runner.
type deviceRunner struct{ dev *device.Device }

func (d deviceRunner) RunCommands(cmds []string) (string, error) {
	var out strings.Builder
	for _, cmd := range cmds {
		out.WriteString(d.dev.Exec(cmd))
	}
	return out.String(), nil
}

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
func (s *Server) SetListener(ln net.Listener) { s.listener = ln }

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

	handler := httphandler.Mux(s.dev.Username, s.dev.Password, deviceRunner{s.dev})
	s.srv = &http.Server{Handler: handler, TLSConfig: tlsCfg}

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
