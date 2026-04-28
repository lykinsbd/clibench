package device

import (
	"os"
	"path/filepath"
	"testing"
)

func setupDevice(t *testing.T) *Device {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "show_version.txt"), []byte("Cisco IOS v15.1\n"), 0644)
	os.WriteFile(filepath.Join(dir, "show_ip_interface_brief.txt"), []byte("Interface  IP-Address\nGi0/0      10.0.0.1\n"), 0644)
	d, err := New("rtr1", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestExecExactMatch(t *testing.T) {
	d := setupDevice(t)
	out := d.Exec("show version")
	if out != "Cisco IOS v15.1\n" {
		t.Errorf("got %q", out)
	}
}

func TestExecPrefixMatch(t *testing.T) {
	d := setupDevice(t)
	out := d.Exec("sh ver")
	if out != "Cisco IOS v15.1\n" {
		t.Errorf("got %q", out)
	}
}

func TestExecUnknown(t *testing.T) {
	d := setupDevice(t)
	out := d.Exec("show bgp")
	if out == "" || out[0] != '%' {
		t.Errorf("expected error output, got %q", out)
	}
}

func TestExecEmpty(t *testing.T) {
	d := setupDevice(t)
	if out := d.Exec(""); out != "" {
		t.Errorf("expected empty, got %q", out)
	}
	if out := d.Exec("   "); out != "" {
		t.Errorf("expected empty for whitespace, got %q", out)
	}
}

func TestExecAmbiguous(t *testing.T) {
	d := setupDevice(t)
	// "sh" matches both "show version" and "show ip interface brief" at word 1,
	// but they have different word counts so neither matches.
	// "show" with 1 word won't match 2-word or 4-word commands.
	out := d.Exec("show")
	if out == "" || out[0] != '%' {
		t.Errorf("expected error for ambiguous/unknown, got %q", out)
	}
}

func TestHostnameSubstitution(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "show_version.txt"), []byte("hostname: {{.Hostname}}\n"), 0644)
	d, err := New("myrouter", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	out := d.Exec("show version")
	if out != "hostname: myrouter\n" {
		t.Errorf("got %q", out)
	}
}

func TestCommands(t *testing.T) {
	d := setupDevice(t)
	cmds := d.Commands()
	if len(cmds) != 2 {
		t.Errorf("expected 2 commands, got %d", len(cmds))
	}
}

func TestPrefixMatchWordCount(t *testing.T) {
	tests := []struct {
		input, full string
		want        bool
	}{
		{"sh ver", "show version", true},
		{"sh ip int br", "show ip interface brief", true},
		{"show version", "show version", true},
		{"sh", "show version", false},       // wrong word count
		{"show ver extra", "show version", false}, // too many words
	}
	for _, tt := range tests {
		got := prefixMatch(tt.input, tt.full)
		if got != tt.want {
			t.Errorf("prefixMatch(%q, %q) = %v, want %v", tt.input, tt.full, got, tt.want)
		}
	}
}
