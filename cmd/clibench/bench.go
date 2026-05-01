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
	"github.com/lykinsbd/clibench/internal/http3server"
	"github.com/lykinsbd/clibench/internal/httpserver"
	latencyPkg "github.com/lykinsbd/clibench/internal/latency"
	"github.com/lykinsbd/clibench/internal/netem"
	"github.com/lykinsbd/clibench/internal/proxy"
	"github.com/lykinsbd/clibench/internal/sshserver"
	"github.com/lykinsbd/clibench/internal/stats"
)

// BenchCmd runs transport benchmarks.
type BenchCmd struct {
	Transport   string `help:"Transport to benchmark (${enum})." enum:"ssh,https,http3,proxy,all" default:"all" short:"t"`
	Iterations  int    `help:"Iterations per benchmark mode." default:"50" short:"n"`
	Concurrency int    `help:"Concurrent workers." default:"1" short:"c"`
	Commands    int    `help:"Commands per iteration." default:"1"`
	Latency     string `help:"Latency profile (${enum})." enum:"local,campus,regional,continental,intercontinental,transpacific" default:"local" short:"l"`
	Userspace   bool   `help:"Use userspace latency injection (no root required)."`
	Output      string `help:"Output format (${enum})." enum:"json,table,csv" default:"json" short:"o"`

	SSHPort   int `help:"SSH listen port." default:"2222" group:"server"`
	HTTPSPort int `help:"HTTPS listen port." default:"8443" group:"server"`
	HTTP3Port int `help:"HTTP/3 listen port." default:"8444" group:"server"`
	ProxyPort int `help:"Proxy listen port." default:"9443" group:"server"`

	User        string `help:"Username." default:"admin" short:"u"`
	Pass        string `help:"Password." default:"admin" short:"p"`
	Transcripts string `help:"Transcript directory." default:"transcripts"`
}

const hostname = "bench-rtr"

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
	campusDelay := 1 * time.Millisecond

	if !b.Userspace && delay > 0 {
		wanPorts := []int{b.SSHPort, b.HTTPSPort, b.HTTP3Port, b.ProxyPort, b.ProxyPort + 1}
		campusPorts := []int{backendSSHPort}
		if err := netem.Setup(delay, campusDelay, wanPorts, campusPorts); err != nil {
			return fmt.Errorf("tc netem setup (requires sudo): %w", err)
		}
		defer netem.Teardown()

		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		go func() {
			<-sigCh
			netem.Teardown()
			os.Exit(1)
		}()

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

	startSSH := func(addr string, d time.Duration) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("listen %s: %v", addr, err)
		}
		srv, err := sshserver.New(addr, dev)
		if err != nil {
			log.Fatalf("ssh %s: %v", addr, err)
		}
		srv.SetListener(wrapListener(ln, d))
		go srv.ListenAndServe()
	}

	startHTTPS := func(addr string, d time.Duration) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("listen %s: %v", addr, err)
		}
		srv := httpserver.New(addr, dev)
		srv.SetListener(wrapListener(ln, d))
		go srv.ListenAndServeTLS()
	}

	startProxy := func(addr, backend string, pooled bool, d time.Duration) {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			log.Fatalf("listen %s: %v", addr, err)
		}
		p := proxy.New(addr, backend, b.User, b.Pass, pooled)
		p.SetListener(wrapListener(ln, d))
		go p.ListenAndServeTLS()
	}

	startSSH(sshAddr, delay)
	startHTTPS(httpsAddr, delay)
	startSSH(backendSSHAddr, campusDelay)
	startProxy(proxyAddr, backendSSHAddr, false, delay)
	startProxy(proxyPooledAddr, backendSSHAddr, true, delay)

	h3srv := http3server.New(http3Addr, dev)
	go h3srv.ListenAndServe()

	time.Sleep(500 * time.Millisecond)
	log.Printf("Server ready — profile=%s, simulated RTT=%.0fms", b.Latency, rttMs)

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

	var results []stats.Result

	if b.Transport == "ssh" || b.Transport == "all" {
		c := cfg
		c.Addr = sshAddr
		results = append(results, bench.SSH(c)...)
	}
	if b.Transport == "https" || b.Transport == "all" {
		c := cfg
		c.Addr = httpsAddr
		results = append(results, bench.HTTPS(c)...)
	}
	if b.Transport == "proxy" || b.Transport == "all" {
		results = append(results, bench.Proxy(bench.ProxyConfig{
			Config:     cfg,
			FreshAddr:  proxyAddr,
			PooledAddr: proxyPooledAddr,
		})...)
	}
	if b.Transport == "http3" || b.Transport == "all" {
		c := cfg
		c.Addr = http3Addr
		results = append(results, bench.HTTP3(c)...)
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
