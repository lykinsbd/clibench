package main

import (
	"encoding/json"
	"flag"
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

const hostname = "bench-rtr"

func main() {
	sshPort := flag.Int("ssh-port", 2222, "SSH listen port (embedded mode)")
	httpsPort := flag.Int("https-port", 8443, "HTTPS listen port (embedded mode)")
	http3Port := flag.Int("http3-port", 8444, "HTTP/3 (QUIC) listen port (embedded mode)")
	user := flag.String("user", "admin", "Username")
	pass := flag.String("pass", "admin", "Password")
	transport := flag.String("transport", "both", "Transport: ssh, https, http3, both, proxy, all")
	iterations := flag.Int("iterations", 50, "Iterations per test")
	concurrency := flag.Int("concurrency", 1, "Concurrent workers")
	commands := flag.Int("commands", 1, "Commands per iteration")
	profile := flag.String("latency", "local", "Latency profile")
	proxyPort := flag.Int("proxy-port", 9443, "Proxy HTTPS listen port")
	transcriptsDir := flag.String("transcripts", "transcripts", "Transcript dir")
	userspace := flag.Bool("userspace", false, "Use userspace latency injection instead of tc netem (no root required)")
	flag.Parse()

	delay, ok := bench.LatencyProfiles[*profile]
	if !ok {
		log.Fatalf("unknown latency profile %q", *profile)
	}
	rttMs := float64(delay.Milliseconds()) * 2

	sshAddr := fmt.Sprintf("localhost:%d", *sshPort)
	httpsAddr := fmt.Sprintf("localhost:%d", *httpsPort)
	http3Addr := fmt.Sprintf("localhost:%d", *http3Port)

	dev, err := device.New(hostname, *user, *pass, *transcriptsDir)
	if err != nil {
		log.Fatalf("device: %v", err)
	}

	backendSSHPort := *sshPort + 1000
	backendSSHAddr := fmt.Sprintf("localhost:%d", backendSSHPort)
	proxyAddr := fmt.Sprintf("localhost:%d", *proxyPort)
	proxyPooledAddr := fmt.Sprintf("localhost:%d", *proxyPort+1)
	campusDelay := 1 * time.Millisecond

	// Set up latency injection
	if !*userspace && delay > 0 {
		wanPorts := []int{*sshPort, *httpsPort, *http3Port, *proxyPort, *proxyPort + 1}
		campusPorts := []int{backendSSHPort}
		if err := netem.Setup(delay, campusDelay, wanPorts, campusPorts); err != nil {
			log.Fatalf("tc netem setup (requires sudo): %v", err)
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
	} else if *userspace && delay > 0 {
		log.Printf("Using userspace latency injection (less accurate than tc netem)")
	}

	wrapListener := func(ln net.Listener, d time.Duration) net.Listener {
		if *userspace && d > 0 {
			return &latencyPkg.Listener{Listener: ln, Delay: d}
		}
		return ln
	}

	// Start servers
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
		p := proxy.New(addr, backend, *user, *pass, pooled)
		p.SetListener(wrapListener(ln, d))
		go p.ListenAndServeTLS()
	}

	startSSH(sshAddr, delay)
	startHTTPS(httpsAddr, delay)
	startSSH(backendSSHAddr, campusDelay)
	startProxy(proxyAddr, backendSSHAddr, false, delay)
	startProxy(proxyPooledAddr, backendSSHAddr, true, delay)

	// Start HTTP/3 server
	h3srv := http3server.New(http3Addr, dev)
	go h3srv.ListenAndServe()

	time.Sleep(500 * time.Millisecond)
	log.Printf("Server ready — profile=%s, simulated RTT=%.0fms", *profile, rttMs)

	cfg := bench.Config{
		User:        *user,
		Pass:        *pass,
		Iterations:  *iterations,
		Concurrency: *concurrency,
		Commands:    *commands,
		Profile:     *profile,
		RTTms:       rttMs,
		Hostname:    hostname,
	}

	var results []stats.Result

	if *transport == "ssh" || *transport == "both" || *transport == "all" {
		c := cfg
		c.Addr = sshAddr
		results = append(results, bench.SSH(c)...)
	}
	if *transport == "https" || *transport == "both" || *transport == "all" {
		c := cfg
		c.Addr = httpsAddr
		results = append(results, bench.HTTPS(c)...)
	}
	if *transport == "proxy" || *transport == "both" || *transport == "all" {
		results = append(results, bench.Proxy(bench.ProxyConfig{
			Config:     cfg,
			FreshAddr:  proxyAddr,
			PooledAddr: proxyPooledAddr,
		})...)
	}
	if *transport == "http3" || *transport == "all" {
		c := cfg
		c.Addr = http3Addr
		results = append(results, bench.HTTP3(c)...)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		log.Fatalf("json encode: %v", err)
	}
}
