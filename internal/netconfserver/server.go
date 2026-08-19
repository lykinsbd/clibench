// Package netconfserver implements a NETCONF 1.1 subsystem server on top of
// crypto/ssh. It handles hello exchange, RFC 6242 chunked framing, and dispatches
// <get>, <edit-config>, and <commit> RPCs to the shared device engine.
package netconfserver

import (
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/sshutil"
	"golang.org/x/crypto/ssh"
)

const (
	// EOM is the NETCONF 1.0 end-of-message delimiter.
	EOM = "]]>]]>"
	// serverHello is the initial hello sent to clients.
	serverHello = `<?xml version="1.0" encoding="UTF-8"?>
<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities>
    <capability>urn:ietf:params:netconf:base:1.1</capability>
    <capability>urn:ietf:params:netconf:capability:writable-running:1.0</capability>
  </capabilities>
  <session-id>1</session-id>
</hello>`
)

// Server is a NETCONF 1.1 server backed by a Device.
type Server struct {
	dev      *device.Device
	addr     string
	listener net.Listener
	sshCfg   *ssh.ServerConfig
}

// New creates a NETCONF server on addr backed by dev.
func New(addr string, dev *device.Device) (*Server, error) {
	cfg, err := sshutil.ServerConfig(dev.Username, dev.Password)
	if err != nil {
		return nil, err
	}
	return &Server{dev: dev, addr: addr, sshCfg: cfg}, nil
}

// SetListener sets a custom net.Listener.
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

// ListenAndServe starts the SSH listener and handles NETCONF subsystem sessions.
func (s *Server) ListenAndServe() error {
	ln, err := sshutil.Listen(s.listener, s.addr, "NETCONF")
	if err != nil {
		return err
	}
	s.listener = ln
	return sshutil.Serve(ln, s.sshCfg, s.handleSession)
}

// handleSession processes a single SSH session channel, waiting for
// a "subsystem" request of "netconf".
func (s *Server) handleSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		switch req.Type {
		case "subsystem":
			name := ""
			if len(req.Payload) >= 4 {
				name = string(req.Payload[4:])
			}
			if name != "netconf" {
				if req.WantReply {
					req.Reply(false, nil)
				}
				continue
			}
			if req.WantReply {
				req.Reply(true, nil)
			}
			s.runSession(ch)
			return
		default:
			if req.WantReply {
				req.Reply(false, nil)
			}
		}
	}
}

// runSession handles the NETCONF protocol on an accepted subsystem channel.
func (s *Server) runSession(ch ssh.Channel) {
	// Send server hello (EOM framing for the hello exchange).
	if _, err := fmt.Fprintf(ch, "%s\n%s", serverHello, EOM); err != nil {
		return
	}

	// Read client hello (EOM-terminated).
	if _, err := readEOM(ch); err != nil {
		return
	}

	// After hello exchange, use chunked framing (RFC 6242 §4.2).
	for {
		msg, err := readChunked(ch)
		if err != nil {
			return
		}
		reply := s.dispatch(msg)
		if err := writeChunked(ch, reply); err != nil {
			return
		}
		// Check if it was a close-session.
		if strings.Contains(string(msg), "<close-session") {
			return
		}
	}
}

// dispatch parses the RPC XML and routes to the appropriate handler.
func (s *Server) dispatch(msg []byte) []byte {
	var rpc rpcMsg
	if err := xml.Unmarshal(msg, &rpc); err != nil {
		return rpcError(rpc.MessageID, "malformed-message", err.Error())
	}

	inner := strings.TrimSpace(rpc.Inner)

	switch {
	case strings.HasPrefix(inner, "<get"):
		return s.handleGet(rpc.MessageID, inner)
	case strings.HasPrefix(inner, "<edit-config"):
		return s.handleEditConfig(rpc.MessageID, inner)
	case strings.HasPrefix(inner, "<commit"):
		return rpcOK(rpc.MessageID)
	case strings.HasPrefix(inner, "<close-session"):
		return rpcOK(rpc.MessageID)
	default:
		return rpcError(rpc.MessageID, "operation-not-supported", "unknown operation")
	}
}

// handleGet executes a <get> RPC, extracting the filter content as CLI commands.
func (s *Server) handleGet(msgID, inner string) []byte {
	cmd := extractFilter(inner)
	output := s.dev.Exec(cmd)
	return []byte(fmt.Sprintf(
		`<rpc-reply message-id="%s" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><data>%s</data></rpc-reply>`,
		msgID, xmlEscape(output)))
}

// handleEditConfig executes an <edit-config> RPC.
func (s *Server) handleEditConfig(msgID, inner string) []byte {
	cmd := extractConfig(inner)
	if cmd != "" {
		s.dev.Exec(cmd)
	}
	return rpcOK(msgID)
}

// --- XML message types ---

type rpcMsg struct {
	XMLName   xml.Name `xml:"rpc"`
	MessageID string   `xml:"message-id,attr"`
	Inner     string   `xml:",innerxml"`
}

// --- helpers ---

func rpcOK(msgID string) []byte {
	return []byte(fmt.Sprintf(
		`<rpc-reply message-id="%s" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><ok/></rpc-reply>`, msgID))
}

func rpcError(msgID, tag, msg string) []byte {
	return []byte(fmt.Sprintf(
		`<rpc-reply message-id="%s" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><rpc-error><error-tag>%s</error-tag><error-message>%s</error-message></rpc-error></rpc-reply>`,
		msgID, tag, msg))
}

// extractFilter pulls the CLI command from a <filter> element.
// e.g., <get><filter type="cli">show ip route</filter></get> → "show ip route"
func extractFilter(inner string) string {
	// Simple extraction — find content between <filter...> and </filter>
	start := strings.Index(inner, ">")
	if start < 0 {
		return "show version"
	}
	// Find the filter element
	filterStart := strings.Index(inner, "<filter")
	if filterStart < 0 {
		return "show version"
	}
	contentStart := strings.Index(inner[filterStart:], ">")
	if contentStart < 0 {
		return "show version"
	}
	contentStart += filterStart + 1
	filterEnd := strings.Index(inner[contentStart:], "</filter>")
	if filterEnd < 0 {
		return "show version"
	}
	return strings.TrimSpace(inner[contentStart : contentStart+filterEnd])
}

// extractConfig pulls CLI commands from an <edit-config> body.
// e.g., <config><cli>interface Gi1\ndescription test</cli></config>
func extractConfig(inner string) string {
	cliStart := strings.Index(inner, "<cli>")
	if cliStart < 0 {
		return ""
	}
	cliStart += 5
	cliEnd := strings.Index(inner[cliStart:], "</cli>")
	if cliEnd < 0 {
		return ""
	}
	return strings.TrimSpace(inner[cliStart : cliStart+cliEnd])
}

func xmlEscape(s string) string {
	var b strings.Builder
	if err := xml.EscapeText(&b, []byte(s)); err != nil {
		return s
	}
	return b.String()
}

// --- RFC 6242 framing ---

// ReadEOM reads until the ]]>]]> delimiter (NETCONF 1.0 hello exchange).
func ReadEOM(r io.Reader) ([]byte, error) {
	return readEOM(r)
}

// ReadChunked reads a chunked-framed message (RFC 6242 §4.2).
func ReadChunked(r io.Reader) ([]byte, error) {
	return readChunked(r)
}

// WriteChunked writes a message using chunked framing.
func WriteChunked(w io.Writer, data []byte) error {
	return writeChunked(w, data)
}

// readEOM reads until the ]]>]]> delimiter (NETCONF 1.0 hello exchange).
func readEOM(r io.Reader) ([]byte, error) {
	var buf []byte
	one := make([]byte, 1)
	for {
		_, err := r.Read(one)
		if err != nil {
			return buf, err
		}
		buf = append(buf, one[0])
		if len(buf) >= len(EOM) && string(buf[len(buf)-len(EOM):]) == EOM {
			return buf[:len(buf)-len(EOM)], nil
		}
		if len(buf) > 1<<20 {
			return nil, fmt.Errorf("hello too large")
		}
	}
}

// readChunked reads a chunked-framed message (RFC 6242 §4.2).
// Format: \n#<len>\n<data>...\n##\n
func readChunked(r io.Reader) ([]byte, error) {
	var msg []byte
	one := make([]byte, 1)

	for {
		// Read \n#
		if err := expect(r, one, '\n'); err != nil {
			return nil, err
		}
		if err := expect(r, one, '#'); err != nil {
			return nil, err
		}
		// Read either a chunk length or '#' (end of message)
		var numBuf []byte
		for {
			if _, err := r.Read(one); err != nil {
				return nil, err
			}
			if one[0] == '\n' {
				break
			}
			numBuf = append(numBuf, one[0])
		}
		if string(numBuf) == "#" {
			return msg, nil
		}
		size, err := strconv.Atoi(string(numBuf))
		if err != nil || size < 1 {
			return nil, fmt.Errorf("invalid chunk size: %q", numBuf)
		}
		chunk := make([]byte, size)
		if _, err := io.ReadFull(r, chunk); err != nil {
			return nil, err
		}
		msg = append(msg, chunk...)
	}
}

// writeChunked writes a message using chunked framing.
func writeChunked(w io.Writer, data []byte) error {
	_, err := fmt.Fprintf(w, "\n#%d\n%s\n##\n", len(data), data)
	return err
}

func expect(r io.Reader, buf []byte, ch byte) error {
	if _, err := r.Read(buf); err != nil {
		return err
	}
	if buf[0] != ch {
		return fmt.Errorf("expected %q, got %q", ch, buf[0])
	}
	return nil
}

// IgnoreHostKey returns an ssh.ClientConfig host key callback that accepts any key.
// Used by benchmark clients.
func IgnoreHostKey() ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return nil
	}
}

// ClientHello returns the XML hello message a NETCONF client sends.
func ClientHello() string {
	return `<?xml version="1.0" encoding="UTF-8"?>
<hello xmlns="urn:ietf:params:xml:ns:netconf:base:1.0">
  <capabilities>
    <capability>urn:ietf:params:netconf:base:1.1</capability>
  </capabilities>
</hello>`
}

// GetRPC builds a <get> RPC with a CLI filter.
func GetRPC(msgID int, cmd string) []byte {
	return []byte(fmt.Sprintf(
		`<rpc message-id="%d" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><get><filter type="cli">%s</filter></get></rpc>`,
		msgID, cmd))
}

// EditConfigRPC builds an <edit-config> RPC with CLI content.
func EditConfigRPC(msgID int, cmds string) []byte {
	return []byte(fmt.Sprintf(
		`<rpc message-id="%d" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><edit-config><target><running/></target><config><cli>%s</cli></config></edit-config></rpc>`,
		msgID, cmds))
}

// CommitRPC builds a <commit> RPC.
func CommitRPC(msgID int) []byte {
	return []byte(fmt.Sprintf(
		`<rpc message-id="%d" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><commit/></rpc>`, msgID))
}

// CloseSessionRPC builds a <close-session> RPC.
func CloseSessionRPC(msgID int) []byte {
	return []byte(fmt.Sprintf(
		`<rpc message-id="%d" xmlns="urn:ietf:params:xml:ns:netconf:base:1.0"><close-session/></rpc>`, msgID))
}
