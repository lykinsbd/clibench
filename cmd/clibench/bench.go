package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lykinsbd/clibench/internal/bench"
	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/headend"
	"github.com/lykinsbd/clibench/internal/http3server"
	"github.com/lykinsbd/clibench/internal/httpserver"
	latencyPkg "github.com/lykinsbd/clibench/internal/latency"
	"github.com/lykinsbd/clibench/internal/netem"
	"github.com/lykinsbd/clibench/internal/pktcount"
	"github.com/lykinsbd/clibench/internal/proxy"
	"github.com/lykinsbd/clibench/internal/sshserver"
	"github.com/lykinsbd/clibench/internal/stats"
)

// BenchCmd runs transport benchmarks.
type BenchCmd struct {
	Transport   []string `help:"Transports to benchmark (${enum}). Comma-separated or repeated." enum:"ssh,https,http3,proxy,tunnel-https,tunnel-http3,all" default:"all" short:"t"`
	Iterations  int      `help:"Iterations per benchmark mode." default:"50" short:"n"`
	Concurrency int      `help:"Concurrent workers." default:"1" short:"c"`
	Commands    int      `help:"Commands per iteration." default:"1"`
	Latency     string   `help:"Latency profile (${enum})." enum:"local,campus,regional,continental,intercontinental,transpacific" default:"local" short:"l"`
	Userspace   bool     `help:"Use userspace latency injection (no root required)."`
	Output      string   `help:"Output format (${enum})." enum:"json,table,csv" default:"json" short:"o"`

	SSHPort          int `help:"SSH listen port." default:"2222" group:"server"`
	HTTPSPort        int `help:"HTTPS listen port." default:"8443" group:"server"`
	HTTP3Port        int `help:"HTTP/3 listen port." default:"8444" group:"server"`
	ProxyPort        int `help:"Proxy listen port." default:"9443" group:"server"`
	HeadendHTTPSPort int `help:"Headend proxy SSH port (HTTPS WAN)." default:"2223" group:"server"`
	HeadendH3Port    int `help:"Headend proxy SSH port (HTTP/3 WAN)." default:"2224" group:"server"`

	User        string `help:"Username." default:"admin" short:"u"`
	Pass        string `help:"Password." default:"admin" short:"p"`
	Transcripts string `help:"Transcript directory." default:"transcripts"`
}

const hostname = "bench-rtr"

func (b *BenchCmd) has(t string) bool {
	for _, v := range b.Transport {
		if v == t || v == "all" {
			return true
		}
	}
	return false
}

// Run executes the benchmark.
func (b *BenchCmd) Run() error {
	delay, ok := bench.LatencyProfiles[b.Latency]
	if !ok {
		return fmt.Errorf("unknown latency profile %q", b.Latency)
	}
	rttMs := float64(delay.Milliseconds()) * 2

	sshAddr := fmt.Sprintf("localhost:%d", b.SSHPort)
	httpsAddr := fmt.Sprintf("localhost:%d", b.HTTPSPort)
	http3Addr := fmt.Sprintf("localhost:%d", b.HTTP3Port)

	dev, err := device.New(hostname, b.User, b.Pass, b.Transcripts)
	if err != nil {
		return fmt.Errorf("device: %w", err)
	}

	backendSSHPort := b.SSHPort + 1000
	backendSSHAddr := fmt.Sprintf("localhost:%d", backendSSHPort)
	proxyAddr := fmt.Sprintf("localhost:%d", b.ProxyPort)
	proxyPooledAddr := fmt.Sprintf("localhost:%d", b.ProxyPort+1)
	headendHTTPSAddr := fmt.Sprintf("localhost:%d", b.HeadendHTTPSPort)
	headendH3Addr := fmt.Sprintf("localhost:%d", b.HeadendH3Port)
	tunnelSiteHTTPSPort := b.ProxyPort + 2
	tunnelSiteHTTPSAddr := fmt.Sprintf("localhost:%d", tunnelSiteHTTPSPort)
	tunnelSiteH3Port := b.ProxyPort + 3
	tunnelSiteH3Addr := fmt.Sprintf("localhost:%d", tunnelSiteH3Port)
	campusDelay := 1 * time.Millisecond

	if !b.Userspace && delay > 0 {
		wanPorts := []int{b.SSHPort, b.HTTPSPort, b.HTTP3Port, b.ProxyPort, b.ProxyPort + 1, tunnelSiteHTTPSPort, tunnelSiteH3Port}
		campusPorts := []int{backendSSHPort, b.HeadendHTTPSPort, b.HeadendH3Port}
		if err := netem.Setup(delay, campusDelay, wanPorts, campusPorts); err != nil {
			return fmt.Errorf("tc netem setup (requires sudo): %w", err)
		}
		defer netem.Teardown()

		log.Printf("tc netem: %dms one-way on ports %v, %dms on ports %v",
			delay.Milliseconds(), wanPorts, campusDelay.Milliseconds(), campusPorts)
	} else if b.Userspace && delay > 0 {
		log.Printf("Using userspace latency injection (less accurate than tc netem)")
	}

	wrapListener := func(ln net.Listener, d time.Duration) net.Listener {
		if b.Userspace && d > 0 {
			return &latencyPkg.Listener{Listener: ln, Delay: d}
		}
		return ln
	}

	startSSH := func(addr string, d time.Duration) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		srv, err := sshserver.New(addr, dev)
		if err != nil {
			ln.Close()
			return fmt.Errorf("ssh %s: %w", addr, err)
		}
		srv.SetListener(wrapListener(ln, d))
		go srv.ListenAndServe()
		return nil
	}

	startHTTPS := func(addr string, d time.Duration) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		srv := httpserver.New(addr, dev)
		srv.SetListener(wrapListener(ln, d))
		go srv.ListenAndServeTLS()
		return nil
	}

	startProxy := func(addr, backend string, pooled bool, d time.Duration) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		p := proxy.New(addr, backend, b.User, b.Pass, pooled)
		p.SetListener(wrapListener(ln, d))
		go p.ListenAndServeTLS()
		return nil
	}

	if err := startSSH(sshAddr, delay); err != nil {
		return err
	}
	if err := startHTTPS(httpsAddr, delay); err != nil {
		return err
	}
	if err := startSSH(backendSSHAddr, campusDelay); err != nil {
		return err
	}
	if err := startProxy(proxyAddr, backendSSHAddr, false, delay); err != nil {
		return err
	}
	if err := startProxy(proxyPooledAddr, backendSSHAddr, true, delay); err != nil {
		return err
	}

	h3srv := http3server.New(http3Addr, dev)
	go h3srv.ListenAndServe()

	// Tunnel: site proxy (HTTPS frontend, WAN delay) → backend SSH (campus delay)
	if err := startProxy(tunnelSiteHTTPSAddr, backendSSHAddr, true, delay); err != nil {
		return err
	}

	// Tunnel: site proxy HTTP/3 (WAN delay) → backend SSH (campus delay)
	tunnelSiteH3 := proxy.New(tunnelSiteH3Addr, backendSSHAddr, b.User, b.Pass, true)
	go tunnelSiteH3.ListenAndServeH3()

	// Tunnel: headend proxies (SSH frontend, campus delay) → site proxy over WAN
	startHeadend := func(addr, backendURL, transport string, d time.Duration) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		srv, err := headend.New(addr, backendURL, b.User, b.Pass, transport)
		if err != nil {
			ln.Close()
			return fmt.Errorf("headend %s: %w", addr, err)
		}
		srv.SetListener(wrapListener(ln, d))
		go srv.ListenAndServe()
		return nil
	}
	if err := startHeadend(headendHTTPSAddr, fmt.Sprintf("https://%s", tunnelSiteHTTPSAddr), "https", campusDelay); err != nil {
		return err
	}
	if err := startHeadend(headendH3Addr, fmt.Sprintf("https://%s", tunnelSiteH3Addr), "http3", campusDelay); err != nil {
		return err
	}

	time.Sleep(500 * time.Millisecond)
	log.Printf("Server ready — profile=%s, simulated RTT=%.0fms", b.Latency, rttMs)

	// Start packet counter if we have root (same requirement as netem)
	var pc *pktcount.Counter
	if !b.Userspace && delay > 0 {
		allPorts := []int{b.SSHPort, b.HTTPSPort, b.HTTP3Port, b.ProxyPort, b.ProxyPort + 1,
			backendSSHPort, tunnelSiteHTTPSPort, tunnelSiteH3Port,
			b.HeadendHTTPSPort, b.HeadendH3Port}
		var err error
		pc, err = pktcount.New(allPorts)
		if err != nil {
			log.Printf("packet counter unavailable: %v", err)
		} else {
			pc.Start()
			defer pc.Stop()
			log.Printf("AF_PACKET: counting packets on ports %v", allPorts)
		}
	}

	// Signal handler for clean shutdown — must be after all resource setup
	// so it can clean up everything that defers would.
	if !b.Userspace && delay > 0 {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			if pc != nil {
				pc.Stop()
			}
			netem.Teardown()
			os.Exit(0)
		}()
		defer signal.Stop(sigCh)
	}

	cfg := bench.Config{
		User:        b.User,
		Pass:        b.Pass,
		Iterations:  b.Iterations,
		Concurrency: b.Concurrency,
		Commands:    b.Commands,
		Profile:     b.Latency,
		RTTms:       rttMs,
		Hostname:    hostname,
	}

	// pktWrap runs a benchmark function and attaches packet counts.
	// Counts are total for the transport (all modes combined).
	pktWrap := func(fn func() []stats.Result) []stats.Result {
		if pc == nil {
			return fn()
		}
		pc.Reset()
		rs := fn()
		totalIn, totalOut := pc.Snapshot()
		// Store total packets on the first result; others get zero.
		// Per-mode granularity would require snapshots inside each bench function.
		if len(rs) > 0 {
			rs[0].PacketsIn = totalIn
			rs[0].PacketsOut = totalOut
		}
		return rs
	}

	var results []stats.Result

	if b.has("ssh") {
		c := cfg
		c.Addr = sshAddr
		results = append(results, pktWrap(func() []stats.Result { return bench.SSH(c) })...)
	}
	if b.has("https") {
		c := cfg
		c.Addr = httpsAddr
		results = append(results, pktWrap(func() []stats.Result { return bench.HTTPS(c) })...)
	}
	if b.has("proxy") {
		results = append(results, pktWrap(func() []stats.Result {
			return bench.Proxy(bench.ProxyConfig{Config: cfg, FreshAddr: proxyAddr, PooledAddr: proxyPooledAddr})
		})...)
	}
	if b.has("http3") {
		c := cfg
		c.Addr = http3Addr
		results = append(results, pktWrap(func() []stats.Result { return bench.HTTP3(c) })...)
	}
	if b.has("tunnel-https") {
		results = append(results, pktWrap(func() []stats.Result {
			return bench.Tunnel(bench.TunnelConfig{Config: cfg, HTTPSHeadendAddr: headendHTTPSAddr})
		})...)
	}
	if b.has("tunnel-http3") {
		results = append(results, pktWrap(func() []stats.Result {
			return bench.Tunnel(bench.TunnelConfig{Config: cfg, H3HeadendAddr: headendH3Addr})
		})...)
	}

	return outputResults(results, b.Output)
}

func outputResults(results []stats.Result, format string) error {
	switch format {
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(results)
	case "table":
		return writeTable(os.Stdout, results)
	case "csv":
		return writeCSV(os.Stdout, results)
	default:
		return fmt.Errorf("unknown output format %q", format)
	}
}
