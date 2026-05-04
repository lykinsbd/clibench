package rtcount

import (
	"net"
	"testing"
)

func TestConnTrips(t *testing.T) {
	s, c := net.Pipe()
	defer s.Close()
	defer c.Close()

	cc := Wrap(c)

	// Write then read = 1 trip
	go func() {
		buf := make([]byte, 5)
		_, _ = s.Read(buf)
		_, _ = s.Write([]byte("world"))
	}()

	_, _ = cc.Write([]byte("hello"))
	buf := make([]byte, 5)
	_, _ = cc.Read(buf)

	if got := cc.Trips(); got != 1 {
		t.Errorf("Trips() = %d, want 1", got)
	}

	// Another write→read = 2 total
	go func() {
		buf := make([]byte, 4)
		_, _ = s.Read(buf)
		_, _ = s.Write([]byte("back"))
	}()

	_, _ = cc.Write([]byte("ping"))
	_, _ = cc.Read(buf[:4])

	if got := cc.Trips(); got != 2 {
		t.Errorf("Trips() = %d, want 2", got)
	}
}

func TestConnConsecutiveWritesCountOnce(t *testing.T) {
	s, c := net.Pipe()
	defer s.Close()
	defer c.Close()

	cc := Wrap(c)

	go func() {
		buf := make([]byte, 10)
		_, _ = s.Read(buf)
		_, _ = s.Read(buf)
		_, _ = s.Write([]byte("ok"))
	}()

	// Two consecutive writes should not increment trips
	_, _ = cc.Write([]byte("a"))
	_, _ = cc.Write([]byte("b"))
	buf := make([]byte, 2)
	_, _ = cc.Read(buf)

	if got := cc.Trips(); got != 1 {
		t.Errorf("Trips() = %d, want 1 (consecutive writes should count as one direction)", got)
	}
}

func TestPacketConnTrips(t *testing.T) {
	a, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()

	cc := WrapPacket(a)

	// Write to b, then read response
	go func() {
		buf := make([]byte, 64)
		n, addr, _ := b.ReadFrom(buf)
		_, _ = b.WriteTo(buf[:n], addr)
	}()

	_, _ = cc.WriteTo([]byte("ping"), b.LocalAddr())
	buf := make([]byte, 64)
	_, _, _ = cc.ReadFrom(buf)

	if got := cc.Trips(); got != 1 {
		t.Errorf("Trips() = %d, want 1", got)
	}
}
