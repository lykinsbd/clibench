// Package restconfserver implements RESTCONF (RFC 8040) endpoints on top of
// the existing HTTPS server infrastructure. It maps YANG-style URL paths to
// CLI commands and wraps responses in JSON or XML envelopes.
package restconfserver

import (
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/tlsutil"
)

// Server is a RESTCONF server backed by a Device.
type Server struct {
	dev      *device.Device
	addr     string
	listener net.Listener
	srv      *http.Server
}

// New creates a RESTCONF server on addr backed by dev.
func New(addr string, dev *device.Device) *Server {
	return &Server{dev: dev, addr: addr}
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

// Close stops the server gracefully.
func (s *Server) Close() error {
	if s.srv != nil {
		return s.srv.Close()
	}
	return nil
}

// ListenAndServeTLS starts the HTTPS listener with a self-signed cert.
func (s *Server) ListenAndServeTLS() error {
	tlsCfg, err := tlsutil.SelfSignedConfig()
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/restconf/data/", s.handleData)
	mux.HandleFunc("/.well-known/host-meta", s.handleHostMeta)

	s.srv = &http.Server{
		Handler:   s.authMiddleware(mux),
		TLSConfig: tlsCfg,
	}

	if s.listener == nil {
		var err error
		s.listener, err = net.Listen("tcp", s.addr)
		if err != nil {
			return err
		}
	}
	ln := tls.NewListener(s.listener, tlsCfg)
	log.Printf("RESTCONF listening on %s", s.listener.Addr())
	err = s.srv.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// authMiddleware checks Basic auth credentials.
func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != s.dev.Username || pass != s.dev.Password {
			w.Header().Set("WWW-Authenticate", `Basic realm="restconf"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleData handles GET and PATCH on /restconf/data/...
func (s *Server) handleData(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGet(w, r)
	case http.MethodPatch:
		s.handlePatch(w, r)
	default:
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
	}
}

// handleGet executes a CLI command derived from the RESTCONF path.
// Path: /restconf/data/cli:show/ip/route → "show ip route"
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	cmd := pathToCommand(r.URL.Path)
	output := s.dev.Exec(cmd)

	accept := r.Header.Get("Accept")
	if strings.Contains(accept, "application/yang-data+xml") {
		w.Header().Set("Content-Type", "application/yang-data+xml")
		writeXMLResponse(w, output)
	} else {
		w.Header().Set("Content-Type", "application/yang-data+json")
		writeJSONResponse(w, output)
	}
}

// handlePatch processes a YANG PATCH request with multiple edit operations.
func (s *Server) handlePatch(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	contentType := r.Header.Get("Content-Type")
	if strings.Contains(contentType, "xml") {
		s.handleXMLPatch(w, body)
	} else {
		s.handleJSONPatch(w, body)
	}
}

// handleJSONPatch processes a JSON YANG PATCH body.
func (s *Server) handleJSONPatch(w http.ResponseWriter, body []byte) {
	var patch struct {
		Edits []struct {
			EditID    string `json:"edit-id"`
			Operation string `json:"operation"`
			Target    string `json:"target"`
			Value     string `json:"value"`
		} `json:"ietf-yang-patch:yang-patch,omitempty"`
	}
	// Try structured format first.
	if err := json.Unmarshal(body, &patch); err != nil || len(patch.Edits) == 0 {
		// Fallback: treat body as newline-delimited commands.
		cmds := strings.Split(strings.TrimSpace(string(body)), "\n")
		for _, cmd := range cmds {
			cmd = strings.TrimSpace(cmd)
			if cmd != "" {
				s.dev.Exec(cmd)
			}
		}
	} else {
		for _, edit := range patch.Edits {
			cmd := strings.TrimPrefix(edit.Target, "/cli:")
			cmd = strings.ReplaceAll(cmd, "/", " ")
			s.dev.Exec(cmd)
		}
	}
	w.Header().Set("Content-Type", "application/yang-data+json")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"ietf-yang-patch:yang-patch-status":{"ok":{}}}`)
}

// handleXMLPatch processes an XML YANG PATCH body.
func (s *Server) handleXMLPatch(w http.ResponseWriter, body []byte) {
	// Simple extraction of edit targets from XML.
	lines := strings.Split(string(body), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "<target>") {
			target := strings.TrimPrefix(line, "<target>")
			target = strings.TrimSuffix(target, "</target>")
			cmd := strings.TrimPrefix(target, "/cli:")
			cmd = strings.ReplaceAll(cmd, "/", " ")
			if cmd != "" {
				s.dev.Exec(cmd)
			}
		}
	}
	w.Header().Set("Content-Type", "application/yang-data+xml")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `<yang-patch-status xmlns="urn:ietf:params:xml:ns:yang:ietf-yang-patch"><ok/></yang-patch-status>`)
}

// handleHostMeta implements RFC 8040 §3.1 discovery.
func (s *Server) handleHostMeta(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/xrd+xml")
	fmt.Fprint(w, `<XRD xmlns="http://docs.oasis-open.org/ns/xri/xrd-1.0"><Link rel="restconf" href="/restconf"/></XRD>`)
}

// --- Response envelope helpers ---

type jsonResponse struct {
	Output struct {
		Response string `json:"response"`
	} `json:"cli:output"`
}

func writeJSONResponse(w http.ResponseWriter, output string) {
	resp := jsonResponse{}
	resp.Output.Response = output
	enc := json.NewEncoder(w)
	enc.Encode(resp) //nolint:errcheck
}

type xmlResponse struct {
	XMLName  xml.Name `xml:"output"`
	XMLNS    string   `xml:"xmlns,attr"`
	Response string   `xml:"response"`
}

func writeXMLResponse(w http.ResponseWriter, output string) {
	resp := xmlResponse{XMLNS: "urn:cli", Response: output}
	w.Write([]byte(xml.Header)) //nolint:errcheck
	enc := xml.NewEncoder(w)
	enc.Encode(resp) //nolint:errcheck
}

// --- Path mapping ---

// pathToCommand converts a RESTCONF URL path to a CLI command.
// /restconf/data/cli:show/ip/route → "show ip route"
func pathToCommand(urlPath string) string {
	// Strip /restconf/data/ prefix.
	path := strings.TrimPrefix(urlPath, "/restconf/data/")
	// Strip cli: namespace prefix.
	path = strings.TrimPrefix(path, "cli:")
	// Convert slashes to spaces.
	cmd := strings.ReplaceAll(path, "/", " ")
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "show version"
	}
	return cmd
}

// CommandToPath converts a CLI command to a RESTCONF URL path.
// "show ip route" → "/restconf/data/cli:show/ip/route"
func CommandToPath(cmd string) string {
	words := strings.Fields(cmd)
	if len(words) == 0 {
		return "/restconf/data/cli:show/version"
	}
	return "/restconf/data/cli:" + strings.Join(words, "/")
}
