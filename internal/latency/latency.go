// Package latency provides a net.Listener wrapper that adds artificial
// delay to connections, simulating real network latency.
//
// The delay model is simplified: each Read or Write that follows a
// direction change (read→write or write→read) incurs the configured
// one-way delay, simulating a network round trip. Consecutive operations
// in the same direction proceed without additional delay, modeling TCP
// stream behavior where multiple writes are coalesced into one flight.
//
// This is more realistic than sleeping on every syscall (which would
// over-penalize protocols that do many small reads), but less accurate
// than kernel-level simulation (tc netem). See the README for a full
// discussion of the tradeoffs.
package latency

import (
	"math/rand"
	"net"
	"sync"
	"time"
)

// Listener wraps a net.Listener, injecting delay on accepted connections.
type Listener struct {
	net.Listener
	Delay  time.Duration
	Jitter time.Duration // standard deviation of added latency variance
	Loss   float64       // packet loss percentage (0-100), approximated
}

// Accept waits for and returns the next connection, wrapped with delay.
func (l *Listener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if l.Delay <= 0 {
		return c, nil
	}
	return &delayConn{Conn: c, delay: l.Delay, jitter: l.Jitter, loss: l.Loss}, nil
}

// delayConn wraps a net.Conn, adding one-way delay on direction changes.
type delayConn struct {
	net.Conn
	delay   time.Duration
	jitter  time.Duration
	loss    float64
	mu      sync.Mutex
	lastDir int // 0=none, 1=read, 2=write
}

// sleepFor returns the delay to apply, adding gaussian jitter and modeling
// loss as an extra full-delay penalty (crude retransmission approximation).
func (c *delayConn) sleepFor() time.Duration {
	d := c.delay
	if c.jitter > 0 {
		// Gaussian variance; clamp to avoid negative sleep.
		d += time.Duration(rand.NormFloat64() * float64(c.jitter)) //nolint:gosec // simulation, not security
		if d < 0 {
			d = 0
		}
	}
	if c.loss > 0 && rand.Float64()*100 < c.loss { //nolint:gosec // simulation, not security
		// Approximate a retransmission: one extra round-trip delay.
		d += c.delay
	}
	return d
}

func (c *delayConn) Read(b []byte) (int, error) {
	c.mu.Lock()
	if c.lastDir != 1 {
		c.lastDir = 1
		c.mu.Unlock()
		time.Sleep(c.sleepFor())
	} else {
		c.mu.Unlock()
	}
	return c.Conn.Read(b)
}

func (c *delayConn) Write(b []byte) (int, error) {
	c.mu.Lock()
	if c.lastDir != 2 {
		c.lastDir = 2
		c.mu.Unlock()
		time.Sleep(c.sleepFor())
	} else {
		c.mu.Unlock()
	}
	return c.Conn.Write(b)
}
