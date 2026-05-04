// Package rtcount provides a net.Conn wrapper that counts application-level
// round trips by tracking I/O direction changes. A direction change from
// write to read (or read to write) indicates a round trip — the client
// sent data and is now waiting for a response.
package rtcount

import (
	"net"
	"sync/atomic"
)

// Conn wraps a net.Conn and counts direction changes.
type Conn struct {
	net.Conn
	trips   atomic.Int64
	lastDir atomic.Int32 // 0=none, 1=read, 2=write
}

// Wrap returns a Conn that counts round trips on c.
func Wrap(c net.Conn) *Conn {
	return &Conn{Conn: c}
}

func (c *Conn) Read(b []byte) (int, error) {
	if c.lastDir.Swap(1) == 2 {
		c.trips.Add(1)
	}
	return c.Conn.Read(b)
}

func (c *Conn) Write(b []byte) (int, error) {
	c.lastDir.Store(2)
	return c.Conn.Write(b)
}

// Trips returns the number of observed round trips.
func (c *Conn) Trips() int {
	return int(c.trips.Load())
}

// PacketConn wraps a net.PacketConn and counts direction changes.
type PacketConn struct {
	net.PacketConn
	trips   atomic.Int64
	lastDir atomic.Int32
}

// WrapPacket returns a PacketConn that counts round trips on pc.
func WrapPacket(pc net.PacketConn) *PacketConn {
	return &PacketConn{PacketConn: pc}
}

func (c *PacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	if c.lastDir.Swap(1) == 2 {
		c.trips.Add(1)
	}
	return c.PacketConn.ReadFrom(b)
}

func (c *PacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	c.lastDir.Store(2)
	return c.PacketConn.WriteTo(b, addr)
}

// Trips returns the number of observed round trips.
func (c *PacketConn) Trips() int {
	return int(c.trips.Load())
}
