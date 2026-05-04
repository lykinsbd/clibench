//go:build pktcount_root

package pktcount

import (
	"net"
	"testing"
	"time"
)

func TestCounterTCP(t *testing.T) {
	// Start a TCP listener on a known port
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	pc, err := New([]int{port})
	if err != nil {
		t.Fatalf("New: %v (requires CAP_NET_RAW)", err)
	}
	pc.Start()
	defer pc.Stop()

	// Accept in background
	go func() {
		c, _ := ln.Accept()
		if c != nil {
			buf := make([]byte, 64)
			_, _ = c.Read(buf)
			_, _ = c.Write([]byte("pong"))
			c.Close()
		}
	}()

	time.Sleep(50 * time.Millisecond)
	pc.Reset()

	// Connect and exchange data
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("ping"))
	buf := make([]byte, 64)
	_, _ = c.Read(buf)
	c.Close()

	time.Sleep(200 * time.Millisecond) // let packets be captured

	in, out := pc.Snapshot()
	t.Logf("packets: in=%d out=%d", in, out)
	if in == 0 {
		t.Error("expected in > 0")
	}
	if out == 0 {
		t.Error("expected out > 0")
	}
}
