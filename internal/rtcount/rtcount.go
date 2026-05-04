// Package rtcount provides net.Conn and net.PacketConn wrappers that count
// application-level round trips (write→read direction changes) and individual
// read/write calls (packet-level I/O operations).
package rtcount

import (
	"net"
	"sync/atomic"
)

// Conn wraps a net.Conn and counts direction changes and I/O calls.
type Conn struct {
	net.Conn
	trips   atomic.Int64
	reads   atomic.Int64
	writes  atomic.Int64
	lastDir atomic.Int32 // 0=none, 1=read, 2=write
}

// Wrap returns a Conn that counts round trips and packets on c.
func Wrap(c net.Conn) *Conn {
	return &Conn{Conn: c}
}

func (c *Conn) Read(b []byte) (int, error) {
	c.reads.Add(1)
	if c.lastDir.Swap(1) == 2 {
		c.trips.Add(1)
	}
	return c.Conn.Read(b)
}

func (c *Conn) Write(b []byte) (int, error) {
	c.writes.Add(1)
	c.lastDir.Store(2)
	return c.Conn.Write(b)
}

// Trips returns the number of observed round trips.
func (c *Conn) Trips() int { return int(c.trips.Load()) }

// Reads returns the number of Read calls (inbound packets).
func (c *Conn) Reads() int { return int(c.reads.Load()) }

// Writes returns the number of Write calls (outbound packets).
func (c *Conn) Writes() int { return int(c.writes.Load()) }

// PacketConn wraps a net.PacketConn and counts direction changes and I/O calls.
type PacketConn struct {
	net.PacketConn
	trips   atomic.Int64
	reads   atomic.Int64
	writes  atomic.Int64
	lastDir atomic.Int32
}

// WrapPacket returns a PacketConn that counts round trips and packets on pc.
func WrapPacket(pc net.PacketConn) *PacketConn {
	return &PacketConn{PacketConn: pc}
}

func (c *PacketConn) ReadFrom(b []byte) (int, net.Addr, error) {
	c.reads.Add(1)
	if c.lastDir.Swap(1) == 2 {
		c.trips.Add(1)
	}
	return c.PacketConn.ReadFrom(b)
}

func (c *PacketConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	c.writes.Add(1)
	c.lastDir.Store(2)
	return c.PacketConn.WriteTo(b, addr)
}

// Trips returns the number of observed round trips.
func (c *PacketConn) Trips() int { return int(c.trips.Load()) }

// Reads returns the number of ReadFrom calls (inbound packets).
func (c *PacketConn) Reads() int { return int(c.reads.Load()) }

// Writes returns the number of WriteTo calls (outbound packets).
func (c *PacketConn) Writes() int { return int(c.writes.Load()) }
