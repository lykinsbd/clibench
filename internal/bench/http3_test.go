package bench

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/http3server"
)

func setupHTTP3Server(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "show_version.txt"), []byte("TestOS v1\n"), 0644)
	os.WriteFile(filepath.Join(dir, "show_ip_interface_brief.txt"), []byte("Gi0/0 10.0.0.1\n"), 0644)
	os.WriteFile(filepath.Join(dir, "terminal_length_0.txt"), []byte(""), 0644)
	os.WriteFile(filepath.Join(dir, "terminal_width_511.txt"), []byte(""), 0644)
	dev, err := device.New("test-rtr", "admin", "admin", dir)
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := http3server.New(conn.LocalAddr().String(), dev)
	srv.SetPacketConn(conn)
	go srv.ListenAndServe()
	time.Sleep(200 * time.Millisecond)
	return conn.LocalAddr().String()
}

func TestHTTP3(t *testing.T) {
	h3Addr := setupHTTP3Server(t)
	cfg := baseCfg(h3Addr)
	results := HTTP3(cfg)
	// fresh-conn, keep-alive, batch-post, 0rtt-resumption = 4 modes
	assertResults(t, results, "http3", 4)
}

func TestHTTP3BadAddr(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := conn.LocalAddr().String()
	conn.Close()

	cfg := baseCfg(addr)
	cfg.Iterations = 1
	cfg.Commands = 1
	results := HTTP3(cfg)
	for _, r := range results {
		if r.Errors == 0 {
			t.Errorf("%s/%s: expected errors connecting to closed port", r.Transport, r.Operation)
		}
	}
}
