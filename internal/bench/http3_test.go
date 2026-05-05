package bench

import (
	"net"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/http3server"
	"github.com/lykinsbd/clibench/internal/testutil"
)

func setupHTTP3Server(t *testing.T) string {
	t.Helper()
	dev := testutil.NewDevice(t)
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := http3server.New(conn.LocalAddr().String(), dev)
	srv.SetPacketConn(conn)
	go srv.ListenAndServe()
	t.Cleanup(func() { srv.Close() })
	time.Sleep(100 * time.Millisecond) // UDP: no dial-based readiness check
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
