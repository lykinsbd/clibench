// Package pktcount captures real packet counts on loopback using AF_PACKET.
// Requires CAP_NET_RAW or root.
package pktcount

import (
	"encoding/binary"
	"sync"
	"sync/atomic"
	"syscall"
)

// Counter captures packets on loopback filtered by port.
type Counter struct {
	fd    int
	ports map[uint16]bool
	in    atomic.Int64 // packets where dst port matches
	out   atomic.Int64 // packets where src port matches
	done  chan struct{}
	wg    sync.WaitGroup
}

// New creates a packet counter for the given ports on loopback.
func New(ports []int) (*Counter, error) {
	// SOCK_DGRAM gives a 4-byte cooked header (protocol family) instead of
	// the 14-byte pseudo-Ethernet header from SOCK_RAW.
	proto := int(htons(syscall.ETH_P_IP))
	fd, err := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_DGRAM, proto)
	if err != nil {
		return nil, err
	}

	// Bind to loopback (ifindex 1)
	sa := &syscall.SockaddrLinklayer{
		Protocol: htons(syscall.ETH_P_IP),
		Ifindex:  1, // lo
	}
	if err := syscall.Bind(fd, sa); err != nil {
		syscall.Close(fd)
		return nil, err
	}

	// Set read timeout once — 100ms lets us check the done channel periodically.
	tv := syscall.Timeval{Sec: 0, Usec: 100_000}
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	// Only count outbound packets (PACKET_OUTGOING is not set for loopback
	// in SOCK_DGRAM mode — loopback delivers each packet once to AF_PACKET).
	// However, on some kernels loopback still delivers twice. We use
	// PACKET_OUTGOING detection via SockaddrLinklayer.Pkttype to deduplicate.

	pm := make(map[uint16]bool, len(ports))
	for _, p := range ports {
		pm[uint16(p)] = true
	}

	return &Counter{fd: fd, ports: pm, done: make(chan struct{})}, nil
}

// Start begins capturing packets in a background goroutine.
func (c *Counter) Start() {
	c.wg.Add(1)
	go c.capture()
}

// Snapshot returns current inbound and outbound packet counts.
func (c *Counter) Snapshot() (in, out int) {
	return int(c.in.Load()), int(c.out.Load())
}

// Reset zeroes the counters.
func (c *Counter) Reset() {
	c.in.Store(0)
	c.out.Store(0)
}

// Stop ends capture and closes the socket.
func (c *Counter) Stop() {
	close(c.done)
	// Close fd to unblock Recvfrom, then wait for goroutine.
	syscall.Close(c.fd)
	c.wg.Wait()
}

func (c *Counter) capture() {
	defer c.wg.Done()
	buf := make([]byte, 65536)
	for {
		select {
		case <-c.done:
			return
		default:
		}

		n, from, err := syscall.Recvfrom(c.fd, buf, 0)
		if err != nil {
			continue
		}
		if n < 20 { // minimum IP header
			continue
		}

		// On loopback, AF_PACKET delivers each packet twice (TX + RX).
		// Filter to only count incoming delivery (Pkttype == PACKET_HOST).
		if sa, ok := from.(*syscall.SockaddrLinklayer); ok {
			if sa.Pkttype == syscall.PACKET_OUTGOING {
				continue // skip TX copy, count only RX delivery
			}
		}

		// SOCK_DGRAM strips the link header; buf starts at IP header.
		srcPort, dstPort, ok := extractPorts(buf[:n])
		if !ok {
			continue
		}

		if c.ports[dstPort] {
			c.in.Add(1)
		}
		if c.ports[srcPort] {
			c.out.Add(1)
		}
	}
}

// extractPorts reads src/dst ports from an IP packet (TCP or UDP).
func extractPorts(ip []byte) (src, dst uint16, ok bool) {
	if len(ip) < 20 {
		return 0, 0, false
	}
	version := ip[0] >> 4
	if version != 4 {
		return 0, 0, false
	}
	ihl := int(ip[0]&0x0f) * 4
	proto := ip[9]
	if proto != 6 && proto != 17 { // TCP or UDP only
		return 0, 0, false
	}
	if len(ip) < ihl+4 {
		return 0, 0, false
	}
	transport := ip[ihl:]
	src = binary.BigEndian.Uint16(transport[0:2])
	dst = binary.BigEndian.Uint16(transport[2:4])
	return src, dst, true
}

func htons(v uint16) uint16 {
	return (v<<8)&0xff00 | v>>8
}
