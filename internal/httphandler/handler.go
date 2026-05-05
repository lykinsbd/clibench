// Package httphandler provides shared HTTP handlers for the ASA-style
// CLI interface used by httpserver, http3server, and proxy.
package httphandler

import (
	"io"
	"net/http"
	"strings"
)

// Runner executes CLI commands and returns their output.
type Runner interface {
	RunCommands(cmds []string) (string, error)
}

// Mux returns an http.Handler with /admin/exec/ and /admin/config routes,
// protected by basic auth.
func Mux(user, pass string, r Runner) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/exec/", execHandler(r))
	mux.HandleFunc("/admin/config", configHandler(r))
	return authMiddleware(user, pass, mux)
}

func authMiddleware(user, pass string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != user || p != pass {
			w.Header().Set("WWW-Authenticate", `Basic realm="device"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func execHandler(r Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		path := strings.TrimPrefix(req.URL.Path, "/admin/exec/")
		if path == "" {
			http.Error(w, "no command", http.StatusBadRequest)
			return
		}
		var cmds []string
		for _, p := range strings.Split(path, "/") {
			cmd := strings.TrimSpace(strings.ReplaceAll(p, "+", " "))
			if cmd != "" {
				cmds = append(cmds, cmd)
			}
		}
		out, err := r.RunCommands(cmds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, out)
	}
}

func configHandler(r Runner) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		if req.Method != http.MethodPost {
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		var cmds []string
		for _, line := range strings.Split(string(body), "\n") {
			cmd := strings.TrimSpace(line)
			if cmd != "" {
				cmds = append(cmds, cmd)
			}
		}
		out, err := r.RunCommands(cmds)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		io.WriteString(w, out)
	}
}
