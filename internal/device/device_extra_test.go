package device

import (
	"testing"
)

func TestNewMissingDir(t *testing.T) {
	_, err := New("rtr1", "admin", "admin", "/nonexistent/path/xyz")
	if err == nil {
		t.Error("expected error for missing dir")
	}
}

func TestNewEmptyDir(t *testing.T) {
	dir := t.TempDir()
	d, err := New("rtr1", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Commands()) != 0 {
		t.Errorf("expected 0 commands, got %d", len(d.Commands()))
	}
}

func TestDeviceNoTranscripts(t *testing.T) {
	dir := t.TempDir()
	d, err := New("rtr1", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	out := d.Exec("show version")
	if out == "" || out[0] != '%' {
		t.Errorf("expected %% error, got %q", out)
	}
}
