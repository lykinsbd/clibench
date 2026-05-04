package pktcount

import "testing"

func TestExtractPortsTCP(t *testing.T) {
	// Minimal IPv4 TCP packet: 20-byte IP header + 4 bytes of TCP (src/dst port)
	ip := make([]byte, 24)
	ip[0] = 0x45       // version=4, IHL=5 (20 bytes)
	ip[9] = 6          // protocol = TCP
	ip[20] = 0x08      // src port = 2222
	ip[21] = 0xAE
	ip[22] = 0x20      // dst port = 8443
	ip[23] = 0xFB

	src, dst, ok := extractPorts(ip)
	if !ok {
		t.Fatal("extractPorts returned false")
	}
	if src != 2222 {
		t.Errorf("src = %d, want 2222", src)
	}
	if dst != 8443 {
		t.Errorf("dst = %d, want 8443", dst)
	}
}

func TestExtractPortsUDP(t *testing.T) {
	ip := make([]byte, 24)
	ip[0] = 0x45
	ip[9] = 17 // UDP
	ip[20] = 0x20
	ip[21] = 0xFC // src port = 8444
	ip[22] = 0xC0
	ip[23] = 0x01 // dst port = 49153

	src, dst, ok := extractPorts(ip)
	if !ok {
		t.Fatal("extractPorts returned false")
	}
	if src != 8444 {
		t.Errorf("src = %d, want 8444", src)
	}
	if dst != 49153 {
		t.Errorf("dst = %d, want 49153", dst)
	}
}

func TestExtractPortsRejectsNonIPv4(t *testing.T) {
	ip := make([]byte, 24)
	ip[0] = 0x60 // IPv6
	_, _, ok := extractPorts(ip)
	if ok {
		t.Error("expected false for IPv6")
	}
}

func TestExtractPortsRejectsICMP(t *testing.T) {
	ip := make([]byte, 24)
	ip[0] = 0x45
	ip[9] = 1 // ICMP
	_, _, ok := extractPorts(ip)
	if ok {
		t.Error("expected false for ICMP")
	}
}

func TestExtractPortsTooShort(t *testing.T) {
	_, _, ok := extractPorts(make([]byte, 10))
	if ok {
		t.Error("expected false for short packet")
	}
}
