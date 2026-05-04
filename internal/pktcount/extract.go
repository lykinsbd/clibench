package pktcount

import "encoding/binary"

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
