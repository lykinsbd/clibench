// Package http3server provides an HTTP/3 (QUIC) server that reuses the
// httphandler logic. Same ASA-style endpoints, different transport.
package http3server

import (
	"log"
	"net"
	"strings"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

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
func (s *Server) SetPacketConn(conn net.PacketConn) { s.conn = conn }

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

	handler := httphandler.Mux(s.dev.Username, s.dev.Password, deviceRunner{s.dev})
	s.srv = &http3.Server{
		Handler:    handler,
		TLSConfig:  tlsCfg,
		QUICConfig: &quic.Config{Allow0RTT: true},
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
