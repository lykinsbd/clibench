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
	Hostname    string `help:"Device hostname." default:"bench-rtr"`
	Transcripts string `help:"Transcript directory." default:"transcripts"`
}

func (b *BenchCmd) has(t string) bool {
	for _, v := range b.Transport {
		if v == t || v == "all" {
			return true
		}
	}
	return false
}

// benchEnv holds all addresses computed from BenchCmd ports.
// Port layout (from base ports):
//   SSHPort          — main SSH server (WAN delay)
//   SSHPort+1000     — backend SSH for proxy/tunnel (campus delay)
//   HTTPSPort        — main HTTPS server (WAN delay)
//   HTTP3Port        — main HTTP/3 server (WAN delay)
//   ProxyPort        — proxy fresh-SSH mode (WAN delay)
//   ProxyPort+1      — proxy pooled-SSH mode (WAN delay)
//   ProxyPort+2      — tunnel site HTTPS proxy (WAN delay)
//   ProxyPort+3      — tunnel site HTTP/3 proxy (WAN delay)
//   ProxyPort+4      — H3 proxy fresh-SSH mode (WAN delay)
//   ProxyPort+5      — H3 proxy pooled-SSH mode (WAN delay)
//   HeadendHTTPSPort — tunnel headend SSH (campus delay)
//   HeadendH3Port    — tunnel headend SSH/H3 (campus delay)
type benchEnv struct {
	sshAddr             string
	httpsAddr           string
	http3Addr           string
	backendSSHPort      int
	backendSSHAddr      string
	proxyAddr           string
	proxyPooledAddr     string
	headendHTTPSAddr    string
	headendH3Addr       string
	tunnelSiteHTTPSPort int
	tunnelSiteHTTPSAddr string
	tunnelSiteH3Port    int
	tunnelSiteH3Addr    string
	proxyH3FreshAddr    string
	proxyH3PooledAddr   string
	delay               time.Duration
	campusDelay         time.Duration
	rttMs               float64
}

func (b *BenchCmd) buildEnv() (*benchEnv, error) {
	delay, ok := bench.LatencyProfiles[b.Latency]
	if !ok {
		return nil, fmt.Errorf("unknown latency profile %q", b.Latency)
	}
	e := &benchEnv{
		sshAddr:             fmt.Sprintf("localhost:%d", b.SSHPort),
		httpsAddr:           fmt.Sprintf("localhost:%d", b.HTTPSPort),
		http3Addr:           fmt.Sprintf("localhost:%d", b.HTTP3Port),
		backendSSHPort:      b.SSHPort + 1000,
		backendSSHAddr:      fmt.Sprintf("localhost:%d", b.SSHPort+1000),
		proxyAddr:           fmt.Sprintf("localhost:%d", b.ProxyPort),
		proxyPooledAddr:     fmt.Sprintf("localhost:%d", b.ProxyPort+1),
		headendHTTPSAddr:    fmt.Sprintf("localhost:%d", b.HeadendHTTPSPort),
		headendH3Addr:       fmt.Sprintf("localhost:%d", b.HeadendH3Port),
		tunnelSiteHTTPSPort: b.ProxyPort + 2,
		tunnelSiteHTTPSAddr: fmt.Sprintf("localhost:%d", b.ProxyPort+2),
		tunnelSiteH3Port:    b.ProxyPort + 3,
		tunnelSiteH3Addr:    fmt.Sprintf("localhost:%d", b.ProxyPort+3),
		proxyH3FreshAddr:    fmt.Sprintf("localhost:%d", b.ProxyPort+4),
		proxyH3PooledAddr:   fmt.Sprintf("localhost:%d", b.ProxyPort+5),
		delay:               delay,
		campusDelay:         1 * time.Millisecond,
		rttMs:               float64(delay.Milliseconds()) * 2,
	}
	return e, nil
}

func (b *BenchCmd) setupLatency(e *benchEnv) error {
	if !b.Userspace && e.delay > 0 {
		wanPorts := []int{b.SSHPort, b.HTTPSPort, b.HTTP3Port, b.ProxyPort, b.ProxyPort + 1, b.ProxyPort + 4, b.ProxyPort + 5, e.tunnelSiteHTTPSPort, e.tunnelSiteH3Port}
		campusPorts := []int{e.backendSSHPort, b.HeadendHTTPSPort, b.HeadendH3Port}
		if err := netem.Setup(e.delay, e.campusDelay, wanPorts, campusPorts); err != nil {
			return fmt.Errorf("tc netem setup (requires sudo): %w", err)
		}
		log.Printf("tc netem: %dms one-way on ports %v, %dms on ports %v",
			e.delay.Milliseconds(), wanPorts, e.campusDelay.Milliseconds(), campusPorts)
	} else if b.Userspace && e.delay > 0 {
		log.Printf("Using userspace latency injection (less accurate than tc netem)")
	}
	return nil
}

func (b *BenchCmd) startServers(e *benchEnv, dev *device.Device) error {
	wrapLn := func(ln net.Listener, d time.Duration) net.Listener {
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
		srv.SetListener(wrapLn(ln, d))
		go srv.ListenAndServe()
		return nil
	}

	startHTTPS := func(addr string, d time.Duration) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		srv := httpserver.New(addr, dev)
		srv.SetListener(wrapLn(ln, d))
		go srv.ListenAndServeTLS()
		return nil
	}

	startProxy := func(addr, backend string, pooled bool, d time.Duration) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		p := proxy.New(addr, backend, b.User, b.Pass, pooled)
		p.SetListener(wrapLn(ln, d))
		go p.ListenAndServeTLS()
		return nil
	}

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
		srv.SetListener(wrapLn(ln, d))
		go srv.ListenAndServe()
		return nil
	}

	// Core servers
	if err := startSSH(e.sshAddr, e.delay); err != nil {
		return err
	}
	if err := startHTTPS(e.httpsAddr, e.delay); err != nil {
		return err
	}
	if err := startSSH(e.backendSSHAddr, e.campusDelay); err != nil {
		return err
	}
	if err := startProxy(e.proxyAddr, e.backendSSHAddr, false, e.delay); err != nil {
		return err
	}
	if err := startProxy(e.proxyPooledAddr, e.backendSSHAddr, true, e.delay); err != nil {
		return err
	}

	h3srv := http3server.New(e.http3Addr, dev)
	go h3srv.ListenAndServe()

	// Tunnel site proxies
	if err := startProxy(e.tunnelSiteHTTPSAddr, e.backendSSHAddr, true, e.delay); err != nil {
		return err
	}
	tunnelSiteH3 := proxy.New(e.tunnelSiteH3Addr, e.backendSSHAddr, b.User, b.Pass, true)
	go tunnelSiteH3.ListenAndServeH3()

	// H3 proxy (fresh + pooled SSH backends)
	proxyH3Fresh := proxy.New(e.proxyH3FreshAddr, e.backendSSHAddr, b.User, b.Pass, false)
	go proxyH3Fresh.ListenAndServeH3()
	proxyH3Pooled := proxy.New(e.proxyH3PooledAddr, e.backendSSHAddr, b.User, b.Pass, true)
	go proxyH3Pooled.ListenAndServeH3()

	// Tunnel headend proxies
	if err := startHeadend(e.headendHTTPSAddr, "https://"+e.tunnelSiteHTTPSAddr, "https", e.campusDelay); err != nil {
		return err
	}
	if err := startHeadend(e.headendH3Addr, "https://"+e.tunnelSiteH3Addr, "http3", e.campusDelay); err != nil {
		return err
	}

	// Wait for TCP servers to be ready (dial timeout must exceed max RTT)
	for _, addr := range []string{e.sshAddr, e.httpsAddr, e.backendSSHAddr, e.proxyAddr, e.headendHTTPSAddr} {
		if err := waitReady(addr, 10*time.Second); err != nil {
			return err
		}
	}
	log.Printf("Servers ready — profile=%s, simulated RTT=%.0fms", b.Latency, e.rttMs)
	return nil
}

func (b *BenchCmd) setupPktCounter(e *benchEnv) (*pktcount.Counter, func()) {
	if b.Userspace {
		return nil, func() {}
	}

	allPorts := []int{b.SSHPort, b.HTTPSPort, b.HTTP3Port, b.ProxyPort, b.ProxyPort + 1,
		b.ProxyPort + 4, b.ProxyPort + 5,
		e.backendSSHPort, e.tunnelSiteHTTPSPort, e.tunnelSiteH3Port,
		b.HeadendHTTPSPort, b.HeadendH3Port}
	pc, err := pktcount.New(allPorts)
	if err != nil {
		log.Printf("packet counter unavailable: %v", err)
		return nil, func() {}
	}
	pc.Start()
	log.Printf("AF_PACKET: counting packets on ports %v", allPorts)

	// Signal handler for clean shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		pc.Stop()
		netem.Teardown()
		os.Exit(0)
	}()

	cleanup := func() {
		signal.Stop(sigCh)
		pc.Stop()
	}
	return pc, cleanup
}

func (b *BenchCmd) runBenchmarks(e *benchEnv, pc *pktcount.Counter) []stats.Result {
	cfg := bench.Config{
		User:        b.User,
		Pass:        b.Pass,
		Iterations:  b.Iterations,
		Concurrency: b.Concurrency,
		Commands:    b.Commands,
		Profile:     b.Latency,
		RTTms:       e.rttMs,
		Hostname:    b.Hostname,
	}
	if pc != nil {
		cfg.PktCounter = pc
	}

	var results []stats.Result

	if b.has("ssh") {
		c := cfg
		c.Addr = e.sshAddr
		results = append(results, bench.SSH(c)...)
	}
	if b.has("https") {
		c := cfg
		c.Addr = e.httpsAddr
		results = append(results, bench.HTTPS(c)...)
	}
	if b.has("proxy") {
		results = append(results, bench.Proxy(bench.ProxyConfig{Config: cfg, FreshAddr: e.proxyAddr, PooledAddr: e.proxyPooledAddr, H3FreshAddr: e.proxyH3FreshAddr, H3PooledAddr: e.proxyH3PooledAddr})...)
	}
	if b.has("http3") {
		c := cfg
		c.Addr = e.http3Addr
		results = append(results, bench.HTTP3(c)...)
	}
	if b.has("tunnel-https") {
		results = append(results, bench.Tunnel(bench.TunnelConfig{Config: cfg, HTTPSHeadendAddr: e.headendHTTPSAddr})...)
	}
	if b.has("tunnel-http3") {
		results = append(results, bench.Tunnel(bench.TunnelConfig{Config: cfg, H3HeadendAddr: e.headendH3Addr})...)
	}

	return results
}

// Run executes the benchmark.
func (b *BenchCmd) Run() error {
	e, err := b.buildEnv()
	if err != nil {
		return err
	}

	if err := b.setupLatency(e); err != nil {
		return err
	}
	if !b.Userspace && e.delay > 0 {
		defer netem.Teardown()
	}

	dev, err := device.New(b.Hostname, b.User, b.Pass, b.Transcripts)
	if err != nil {
		return fmt.Errorf("device: %w", err)
	}

	if err := b.startServers(e, dev); err != nil {
		return err
	}

	pc, cleanup := b.setupPktCounter(e)
	defer cleanup()

	results := b.runBenchmarks(e, pc)
	return outputResults(results, b.Output)
}

// waitReady polls a TCP address until it accepts connections or timeout.
func waitReady(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 1*time.Second)
		if err == nil {
			c.Close()
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("server %s not ready after %v", addr, timeout)
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
