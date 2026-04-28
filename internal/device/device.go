// Package device provides a fake network device that matches commands
// to transcript responses. Both the SSH and HTTPS servers use this
// as their shared backend.
package device

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Device is a fake network device with canned command responses.
type Device struct {
	Hostname   string
	Username   string
	Password   string
	commands   map[string]string // full command -> response text
	transcriptDir string
}

// New creates a Device, loading transcripts from dir.
func New(hostname, username, password, transcriptDir string) (*Device, error) {
	d := &Device{
		Hostname:      hostname,
		Username:      username,
		Password:      password,
		commands:       make(map[string]string),
		transcriptDir: transcriptDir,
	}
	if err := d.loadTranscripts(); err != nil {
		return nil, err
	}
	return d, nil
}

func (d *Device) loadTranscripts() error {
	entries, err := os.ReadDir(d.transcriptDir)
	if err != nil {
		return fmt.Errorf("reading transcript dir: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(d.transcriptDir, e.Name()))
		if err != nil {
			return err
		}
		// filename convention: "show_version.txt" -> command "show version"
		cmd := strings.TrimSuffix(e.Name(), ".txt")
		cmd = strings.ReplaceAll(cmd, "_", " ")
		d.commands[cmd] = strings.ReplaceAll(string(data), "{{.Hostname}}", d.Hostname)
	}
	return nil
}

// Exec runs a command string and returns the output.
// It supports prefix matching (e.g. "sh ver" -> "show version").
func (d *Device) Exec(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}

	// exact match first
	if resp, ok := d.commands[input]; ok {
		return resp
	}

	// prefix match
	var match string
	for cmd := range d.commands {
		if prefixMatch(input, cmd) {
			if match != "" {
				return fmt.Sprintf("%% Ambiguous command: \"%s\"\n", input)
			}
			match = cmd
		}
	}
	if match != "" {
		return d.commands[match]
	}

	return fmt.Sprintf("%% Unknown command: \"%s\"\n", input)
}

// Commands returns the list of supported command names.
func (d *Device) Commands() []string {
	out := make([]string, 0, len(d.commands))
	for cmd := range d.commands {
		out = append(out, cmd)
	}
	return out
}

// prefixMatch checks if input is a valid abbreviation of full.
// "sh ver" matches "show version", "sh ip int br" matches "show ip interface brief".
func prefixMatch(input, full string) bool {
	iWords := strings.Fields(input)
	fWords := strings.Fields(full)
	if len(iWords) != len(fWords) {
		return false
	}
	for i, iw := range iWords {
		if !strings.HasPrefix(fWords[i], iw) {
			return false
		}
	}
	return true
}
