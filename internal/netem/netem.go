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

// Setup configures tc netem on the loopback interface with per-port delays.
func Setup(wanDelay, campusDelay time.Duration, wanPorts, campusPorts []int) error {
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

	if wanDelay > 0 {
		if err := addNetemBand(2, wanDelay, wanPorts); err != nil {
			Teardown()
			return fmt.Errorf("wan band: %w", err)
		}
	}
	if campusDelay > 0 {
		if err := addNetemBand(3, campusDelay, campusPorts); err != nil {
			Teardown()
			return fmt.Errorf("campus band: %w", err)
		}
	}
	return nil
}

func addNetemBand(band uint16, delay time.Duration, ports []int) error {
	netem := netlink.NewNetem(
		netlink.QdiscAttrs{
			LinkIndex: loopbackIndex,
			Handle:    netlink.MakeHandle(band*10, 0),
			Parent:    netlink.MakeHandle(1, band),
		},
		netlink.NetemQdiscAttrs{Latency: uint32(delay.Microseconds())},
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
	if err := addU32Filter(uint32(port), 0xffff, 20, band); err != nil {
		return fmt.Errorf("dport %d: %w", port, err)
	}
	// sport: upper 16 bits at offset 20
	if err := addU32Filter(uint32(port)<<16, 0xffff0000, 20, band); err != nil {
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
