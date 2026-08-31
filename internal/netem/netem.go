//go:build linux

// Package netem provides tc netem-based latency injection on Linux.
// Requires CAP_NET_ADMIN (or root). Applies per-port delay on the
// loopback interface using a prio qdisc with u32 filters, configured
// entirely via netlink (no shell-out to tc).
package netem

import (
	"fmt"
	"time"

	"github.com/vishvananda/netlink"
)

const loopbackIndex = 1

// Config holds the netem parameters for a benchmark run.
// Jitter and Loss apply to the WAN band only; the campus band models a
// fixed local link with deterministic delay.
type Config struct {
	WANDelay    time.Duration
	CampusDelay time.Duration
	WANPorts    []int
	CampusPorts []int
	Jitter      time.Duration // latency standard deviation (WAN band)
	Loss        float64       // packet loss percentage, e.g. 1.0 = 1% (WAN band)
}

// Setup configures tc netem on the loopback interface with per-port delays.
func Setup(c Config) error {
	Teardown()

	prio := netlink.NewPrio(netlink.QdiscAttrs{
		LinkIndex: loopbackIndex,
		Handle:    netlink.MakeHandle(1, 0),
		Parent:    netlink.HANDLE_ROOT,
	})
	prio.Bands = 4
	prio.PriorityMap = [16]uint8{} // all unmatched traffic → band 0 (no delay)
	if err := netlink.QdiscAdd(prio); err != nil {
		return fmt.Errorf("add prio qdisc: %w", err)
	}

	if c.WANDelay > 0 {
		if err := addNetemBand(2, c.WANDelay, c.Jitter, c.Loss, c.WANPorts); err != nil {
			Teardown()
			return fmt.Errorf("wan band: %w", err)
		}
	}
	if c.CampusDelay > 0 {
		// Campus band models a fixed local link — no jitter or loss.
		if err := addNetemBand(3, c.CampusDelay, 0, 0, c.CampusPorts); err != nil {
			Teardown()
			return fmt.Errorf("campus band: %w", err)
		}
	}
	return nil
}

func addNetemBand(band uint16, delay, jitter time.Duration, loss float64, ports []int) error {
	netem := netlink.NewNetem(
		netlink.QdiscAttrs{
			LinkIndex: loopbackIndex,
			Handle:    netlink.MakeHandle(band*10, 0),
			Parent:    netlink.MakeHandle(1, band),
		},
		netlink.NetemQdiscAttrs{
			Latency: uint32(delay.Microseconds()),  //nolint:gosec // bounded: max 600ms = 600000μs, fits uint32
			Jitter:  uint32(jitter.Microseconds()), //nolint:gosec // bounded jitter value fits uint32
			Loss:    float32(loss),
		},
	)
	if err := netlink.QdiscAdd(netem); err != nil {
		return fmt.Errorf("add netem delay %v: %w", delay, err)
	}
	for _, port := range ports {
		if err := addPortFilter(port, band); err != nil {
			return err
		}
	}
	return nil
}

func addPortFilter(port int, band uint16) error {
	// dport: lower 16 bits at offset 20 (TCP/UDP header after 20-byte IP header)
	if err := addU32Filter(uint32(port), 0xffff, 20, band); err != nil { //nolint:gosec // port is 0-65535
		return fmt.Errorf("dport %d: %w", port, err)
	}
	// sport: upper 16 bits at offset 20
	if err := addU32Filter(uint32(port)<<16, 0xffff0000, 20, band); err != nil { //nolint:gosec // port is 0-65535
		return fmt.Errorf("sport %d: %w", port, err)
	}
	return nil
}

func addU32Filter(val, mask uint32, off int32, band uint16) error {
	filter := &netlink.U32{
		FilterAttrs: netlink.FilterAttrs{
			LinkIndex: loopbackIndex,
			Parent:    netlink.MakeHandle(1, 0),
			Priority:  1,
			Protocol:  0x0800, // ETH_P_IP
		},
		ClassId: netlink.MakeHandle(1, band),
		Sel: &netlink.TcU32Sel{
			Nkeys: 1,
			Flags: netlink.TC_U32_TERMINAL,
			Keys: []netlink.TcU32Key{{
				Val:  val,
				Mask: mask,
				Off:  off,
			}},
		},
	}
	return netlink.FilterAdd(filter)
}

// Teardown removes the tc qdisc from loopback.
func Teardown() {
	netlink.QdiscDel(netlink.NewPrio(netlink.QdiscAttrs{
		LinkIndex: loopbackIndex,
		Handle:    netlink.MakeHandle(1, 0),
		Parent:    netlink.HANDLE_ROOT,
	}))
}
