package latency

import (
	"net"
	"testing"
	"time"
)

func TestDelayConnDirectionChange(t *testing.T) {
	// Create a pipe to test delay behavior
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	delay := 20 * time.Millisecond
	dc := &delayConn{Conn: client, delay: delay}

	// First write should incur delay (direction change from none→write)
	go func() {
		buf := make([]byte, 64)
		server.Read(buf)
		server.Write([]byte("ok"))
	}()

	start := time.Now()
	dc.Write([]byte("hello"))
	elapsed := time.Since(start)
	if elapsed < delay/2 {
		t.Errorf("first write too fast: %v", elapsed)
	}

	// Second write (same direction) should NOT incur delay
	go func() {
		buf := make([]byte, 64)
		server.Read(buf)
	}()
	start = time.Now()
	dc.Write([]byte("world"))
	elapsed = time.Since(start)
	if elapsed > delay/2 {
		t.Errorf("consecutive write should be fast, took %v", elapsed)
	}
}

func TestDelayConnReadAfterWrite(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	delay := 20 * time.Millisecond
	dc := &delayConn{Conn: client, delay: delay}

	// Write first
	go func() {
		buf := make([]byte, 64)
		server.Read(buf)
		server.Write([]byte("response"))
	}()
	dc.Write([]byte("request"))

	// Read should incur delay (direction change write→read)
	start := time.Now()
	buf := make([]byte, 64)
	dc.Read(buf)
	elapsed := time.Since(start)
	if elapsed < delay/2 {
		t.Errorf("read after write should be delayed, took %v", elapsed)
	}
}

func TestListenerNoDelay(t *testing.T) {
	inner, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer inner.Close()

	ln := &Listener{Listener: inner, Delay: 0}
	go func() {
		net.Dial("tcp", inner.Addr().String())
	}()
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	// With zero delay, should return a plain net.Conn, not a delayConn
	if _, ok := conn.(*delayConn); ok {
		t.Error("zero delay should not wrap connection")
	}
}
