package main

import (
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/httpserver"
	latencyPkg "github.com/lykinsbd/clibench/internal/latency"
	"github.com/lykinsbd/clibench/internal/netem"
	"github.com/lykinsbd/clibench/internal/proxy"
	"github.com/lykinsbd/clibench/internal/sshserver"
	"github.com/lykinsbd/clibench/internal/stats"
	"golang.org/x/crypto/ssh"
)

// errDuration is a sentinel value indicating a failed iteration.
var errDuration = stats.ErrDuration

// Latency profiles sourced from Verizon Enterprise monthly backbone
// measurements (March 2026) and AWS/RIPE Atlas data.
// See plans/ssh-vs-https-cli/latency-profiles.md for full citations.
var latencyProfiles = map[string]time.Duration{
	"local":            0,
	"campus":           1 * time.Millisecond,
	"regional":         15 * time.Millisecond,
	"continental":      35 * time.Millisecond,
	"intercontinental": 75 * time.Millisecond,
	"transpacific":     87 * time.Millisecond,
}

func main() {
	sshPort := flag.Int("ssh-port", 2222, "SSH listen port (embedded mode)")
	httpsPort := flag.Int("https-port", 8443, "HTTPS listen port (embedded mode)")
	user := flag.String("user", "admin", "Username")
	pass := flag.String("pass", "admin", "Password")
	transport := flag.String("transport", "both", "Transport: ssh, https, both, proxy")
	iterations := flag.Int("iterations", 50, "Iterations per test")
	concurrency := flag.Int("concurrency", 1, "Concurrent workers")
	commands := flag.Int("commands", 1, "Commands per iteration")
	profile := flag.String("latency", "local", "Latency profile")
	proxyPort := flag.Int("proxy-port", 9443, "Proxy HTTPS listen port")
	transcriptsDir := flag.String("transcripts", "transcripts", "Transcript dir")
	userspace := flag.Bool("userspace", false, "Use userspace latency injection instead of tc netem (no root required)")
	flag.Parse()

	delay, ok := latencyProfiles[*profile]
	if !ok {
		log.Fatalf("unknown latency profile %q", *profile)
	}
	rttMs := float64(delay.Milliseconds()) * 2

	sshAddr := fmt.Sprintf("localhost:%d", *sshPort)
	httpsAddr := fmt.Sprintf("localhost:%d", *httpsPort)

	dev, err := device.New("bench-rtr", *user, *pass, *transcriptsDir)
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
		// tc netem: kernel-level delay on loopback, per-port
		wanPorts := []int{*sshPort, *httpsPort, *proxyPort, *proxyPort + 1}
		campusPorts := []int{backendSSHPort}
		if err := netem.Setup(delay, campusDelay, wanPorts, campusPorts); err != nil {
			log.Fatalf("tc netem setup (requires sudo): %v", err)
		}
		defer netem.Teardown()
		log.Printf("tc netem: %dms one-way on ports %v, %dms on ports %v",
			delay.Milliseconds(), wanPorts, campusDelay.Milliseconds(), campusPorts)
	} else if *userspace && delay > 0 {
		log.Printf("Using userspace latency injection (less accurate than tc netem)")
	}

	// wrapListener applies userspace delay if in userspace mode, otherwise returns the listener as-is.
	wrapListener := func(ln net.Listener, d time.Duration) net.Listener {
		if *userspace && d > 0 {
			return &latencyPkg.Listener{Listener: ln, Delay: d}
		}
		return ln
	}

	// Start SSH server
	sshLn, err := net.Listen("tcp", sshAddr)
	if err != nil {
		log.Fatalf("ssh listen: %v", err)
	}
	sshSrv, err := sshserver.New(sshAddr, dev)
	if err != nil {
		log.Fatalf("ssh: %v", err)
	}
	sshSrv.SetListener(wrapListener(sshLn, delay))
	go sshSrv.ListenAndServe()

	// Start HTTPS server
	httpsLn, err := net.Listen("tcp", httpsAddr)
	if err != nil {
		log.Fatalf("https listen: %v", err)
	}
	httpSrv := httpserver.New(httpsAddr, dev)
	httpSrv.SetListener(wrapListener(httpsLn, delay))
	go httpSrv.ListenAndServeTLS()

	// Proxy: HTTPS frontend (WAN latency) → SSH backend (campus latency)
	backendLn, err := net.Listen("tcp", backendSSHAddr)
	if err != nil {
		log.Fatalf("backend ssh listen: %v", err)
	}
	backendSrv, err := sshserver.New(backendSSHAddr, dev)
	if err != nil {
		log.Fatalf("backend ssh: %v", err)
	}
	backendSrv.SetListener(wrapListener(backendLn, campusDelay))
	go backendSrv.ListenAndServe()

	proxyLn, err := net.Listen("tcp", proxyAddr)
	if err != nil {
		log.Fatalf("proxy listen: %v", err)
	}
	proxyFresh := proxy.New(proxyAddr, backendSSHAddr, *user, *pass, false)
	proxyFresh.SetListener(wrapListener(proxyLn, delay))
	go proxyFresh.ListenAndServeTLS()

	proxyPooledLn, err := net.Listen("tcp", proxyPooledAddr)
	if err != nil {
		log.Fatalf("proxy-pooled listen: %v", err)
	}
	proxyPooled := proxy.New(proxyPooledAddr, backendSSHAddr, *user, *pass, true)
	proxyPooled.SetListener(wrapListener(proxyPooledLn, delay))
	go proxyPooled.ListenAndServeTLS()

	time.Sleep(500 * time.Millisecond)
	log.Printf("Server ready — profile=%s, simulated RTT=%.0fms", *profile, rttMs)

	var results []stats.Result

	if *transport == "ssh" || *transport == "both" {
		r := benchSSH(sshAddr, *user, *pass, *iterations, *concurrency, *commands, *profile, rttMs)
		results = append(results, r...)
	}
	if *transport == "https" || *transport == "both" {
		r := benchHTTPS(httpsAddr, *user, *pass, *iterations, *concurrency, *commands, *profile, rttMs)
		results = append(results, r...)
	}
	if *transport == "proxy" || *transport == "both" {
		r := benchProxy(proxyAddr, proxyPooledAddr, *user, *pass, *iterations, *concurrency, *commands, *profile, rttMs)
		results = append(results, r...)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(results); err != nil {
		log.Fatalf("json encode: %v", err)
	}
}

func sshConfig(user, pass string) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
}

func benchSSH(addr, user, pass string, iterations, concurrency, cmdsPerIter int, profile string, rttMs float64) []stats.Result {
	log.Printf("Benchmarking SSH (%d iterations, %d concurrency, %d cmds/iter)", iterations, concurrency, cmdsPerIter)
	cfg := sshConfig(user, pass)

	// Mode 1: fresh connection per iteration
	freshTimes := stats.RunParallel(iterations, concurrency, func() time.Duration {
		start := time.Now()
		conn, err := ssh.Dial("tcp", addr, cfg)
		if err != nil {
			log.Printf("ssh dial: %v", err)
			return errDuration
		}
		defer conn.Close()
		for i := 0; i < cmdsPerIter; i++ {
			sess, err := conn.NewSession()
			if err != nil {
				log.Printf("ssh session: %v", err)
				return errDuration
			}
			_, err = sess.Output("show version")
			sess.Close()
			if err != nil {
				log.Printf("ssh exec: %v", err)
				return errDuration
			}
		}
		return time.Since(start)
	})

	// Mode 2: reuse one connection (ControlMaster-style)
	// Warmup: establish connection + one throwaway iteration
	sharedConn, err := ssh.Dial("tcp", addr, cfg)
	var reuseTimes []time.Duration
	if err != nil {
		log.Printf("ssh reuse dial: %v (skipping reuse test)", err)
	} else {
		if sess, err := sharedConn.NewSession(); err == nil {
			sess.Output("show version")
			sess.Close()
		}
		reuseTimes = stats.RunParallel(iterations, concurrency, func() time.Duration {
			start := time.Now()
			for i := 0; i < cmdsPerIter; i++ {
				sess, err := sharedConn.NewSession()
				if err != nil {
					log.Printf("ssh reuse session: %v", err)
					return errDuration
				}
				_, err = sess.Output("show version")
				sess.Close()
				if err != nil {
					log.Printf("ssh reuse exec: %v", err)
					return errDuration
				}
			}
			return time.Since(start)
		})
		sharedConn.Close()
	}

	// Mode 3: batch exec — send multi-line payload over a single exec session
	batchPayload := stats.GenerateExecPayload(cmdsPerIter)
	batchTimes := stats.RunParallel(iterations, concurrency, func() time.Duration {
		start := time.Now()
		conn, err := ssh.Dial("tcp", addr, cfg)
		if err != nil {
			log.Printf("ssh batch dial: %v", err)
			return errDuration
		}
		defer conn.Close()
		sess, err := conn.NewSession()
		if err != nil {
			log.Printf("ssh batch session: %v", err)
			return errDuration
		}
		_, err = sess.Output(batchPayload)
		sess.Close()
		if err != nil {
			log.Printf("ssh batch exec: %v", err)
			return errDuration
		}
		return time.Since(start)
	})

	results := []stats.Result{
		stats.Summarize("ssh", "fresh-conn", cmdsPerIter, iterations, concurrency, profile, rttMs, freshTimes),
	}
	if reuseTimes != nil {
		results = append(results, stats.Summarize("ssh", "reuse-conn", cmdsPerIter, iterations, concurrency, profile, rttMs, reuseTimes))
	}
	results = append(results, stats.Summarize("ssh", "batch-exec", cmdsPerIter, iterations, concurrency, profile, rttMs, batchTimes))

	// Mode 4: PTY/shell — interactive session with prompt detection (Netmiko-style)
	// Includes session prep (terminal length 0, terminal width 511) and
	// per-command echo verification, matching real-world tool behavior.
	prompt := "bench-rtr#"
	ptyFreshTimes := stats.RunParallel(iterations, concurrency, func() time.Duration {
		start := time.Now()
		conn, err := ssh.Dial("tcp", addr, cfg)
		if err != nil {
			log.Printf("ssh pty dial: %v", err)
			return errDuration
		}
		defer conn.Close()
		sess, err := conn.NewSession()
		if err != nil {
			return errDuration
		}
		defer sess.Close()
		if err := ptyExecCmds(sess, prompt, cmdsPerIter); err != nil {
			log.Printf("ssh pty-fresh: %v", err)
			return errDuration
		}
		return time.Since(start)
	})
	results = append(results, stats.Summarize("ssh", "pty-fresh", cmdsPerIter, iterations, concurrency, profile, rttMs, ptyFreshTimes))

	// Mode 5: PTY/shell — reuse connection, new shell per iteration
	ptyConn, err := ssh.Dial("tcp", addr, cfg)
	if err == nil {
		ptyReuseTimes := stats.RunParallel(iterations, concurrency, func() time.Duration {
			start := time.Now()
			sess, err := ptyConn.NewSession()
			if err != nil {
				return errDuration
			}
			defer sess.Close()
			if err := ptyExecCmds(sess, prompt, cmdsPerIter); err != nil {
				log.Printf("ssh pty-reuse: %v", err)
				return errDuration
			}
			return time.Since(start)
		})
		ptyConn.Close()
		results = append(results, stats.Summarize("ssh", "pty-reuse", cmdsPerIter, iterations, concurrency, profile, rttMs, ptyReuseTimes))
	}

	return results
}

func benchHTTPS(addr, user, pass string, iterations, concurrency, cmdsPerIter int, profile string, rttMs float64) []stats.Result {
	log.Printf("Benchmarking HTTPS (%d iterations, %d concurrency, %d cmds/iter)", iterations, concurrency, cmdsPerIter)

	tlsCfg := &tls.Config{InsecureSkipVerify: true}

	// Mode 1: fresh connection per iteration (DisableKeepAlives)
	freshTimes := stats.RunParallel(iterations, concurrency, func() time.Duration {
		start := time.Now()
		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:   tlsCfg,
				DisableKeepAlives: true,
			},
			Timeout: 30 * time.Second,
		}
		for i := 0; i < cmdsPerIter; i++ {
			if err := doHTTPExec(client, addr, user, pass); err != nil {
				log.Printf("https fresh: %v", err)
				return errDuration
			}
		}
		return time.Since(start)
	})

	// Mode 2: keep-alive — shared client across ALL iterations
	keepAliveClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   30 * time.Second,
	}
	// Warmup: one request to establish the TLS session
	doHTTPExec(keepAliveClient, addr, user, pass)

	reuseTimes := stats.RunParallel(iterations, concurrency, func() time.Duration {
		start := time.Now()
		for i := 0; i < cmdsPerIter; i++ {
			if err := doHTTPExec(keepAliveClient, addr, user, pass); err != nil {
				log.Printf("https keep-alive: %v", err)
				return errDuration
			}
		}
		return time.Since(start)
	})

	// Mode 3: batch POST — all commands in one request body
	batchPayload := stats.GenerateExecPayload(cmdsPerIter)
	batchTimes := stats.RunParallel(iterations, concurrency, func() time.Duration {
		start := time.Now()
		client := &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
			Timeout:   30 * time.Second,
		}
		url := fmt.Sprintf("https://%s/admin/config", addr)
		req, err := http.NewRequest("POST", url, strings.NewReader(batchPayload))
		if err != nil {
			return errDuration
		}
		req.SetBasicAuth(user, pass)
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("https batch: %v", err)
			return errDuration
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		return time.Since(start)
	})

	results := []stats.Result{
		stats.Summarize("https", "fresh-conn", cmdsPerIter, iterations, concurrency, profile, rttMs, freshTimes),
		stats.Summarize("https", "keep-alive", cmdsPerIter, iterations, concurrency, profile, rttMs, reuseTimes),
		stats.Summarize("https", "batch-post", cmdsPerIter, iterations, concurrency, profile, rttMs, batchTimes),
	}

	// Mode 4: multi-command GET (ASA slash syntax) — only if >1 cmd
	if cmdsPerIter > 1 {
		cmdParts := make([]string, cmdsPerIter)
		for i := range cmdParts {
			cmdParts[i] = "show+version"
		}
		multiPath := strings.Join(cmdParts, "/")

		multiTimes := stats.RunParallel(iterations, concurrency, func() time.Duration {
			start := time.Now()
			client := &http.Client{
				Transport: &http.Transport{TLSClientConfig: tlsCfg},
				Timeout:   30 * time.Second,
			}
			url := fmt.Sprintf("https://%s/admin/exec/%s", addr, multiPath)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				return errDuration
			}
			req.SetBasicAuth(user, pass)
			resp, err := client.Do(req)
			if err != nil {
				log.Printf("https multi: %v", err)
				return errDuration
			}
			io.ReadAll(resp.Body)
			resp.Body.Close()
			return time.Since(start)
		})
		results = append(results, stats.Summarize("https", "multi-cmd", cmdsPerIter, iterations, concurrency, profile, rttMs, multiTimes))
	}

	return results
}

func benchProxy(freshAddr, pooledAddr, user, pass string, iterations, concurrency, cmdsPerIter int, profile string, rttMs float64) []stats.Result {
	log.Printf("Benchmarking Proxy (%d iterations, %d concurrency, %d cmds/iter)", iterations, concurrency, cmdsPerIter)

	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	payload := stats.GenerateExecPayload(cmdsPerIter)

	doProxy := func(addr string) time.Duration {
		start := time.Now()
		client := &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
			Timeout:   30 * time.Second,
		}
		url := fmt.Sprintf("https://%s/admin/config", addr)
		req, err := http.NewRequest("POST", url, strings.NewReader(payload))
		if err != nil {
			return errDuration
		}
		req.SetBasicAuth(user, pass)
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("proxy: %v", err)
			return errDuration
		}
		io.ReadAll(resp.Body)
		resp.Body.Close()
		return time.Since(start)
	}

	freshTimes := stats.RunParallel(iterations, concurrency, func() time.Duration { return doProxy(freshAddr) })
	pooledTimes := stats.RunParallel(iterations, concurrency, func() time.Duration { return doProxy(pooledAddr) })

	return []stats.Result{
		stats.Summarize("proxy", "fresh-ssh", cmdsPerIter, iterations, concurrency, profile, rttMs, freshTimes),
		stats.Summarize("proxy", "pooled-ssh", cmdsPerIter, iterations, concurrency, profile, rttMs, pooledTimes),
	}
}

// doHTTPExec sends a single show+version GET and drains the response.
func doHTTPExec(client *http.Client, addr, user, pass string) error {
	url := fmt.Sprintf("https://%s/admin/exec/show+version", addr)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	io.ReadAll(resp.Body)
	resp.Body.Close()
	return nil
}

// ptyReader wraps an io.Reader with a buffer so readUntil doesn't lose
// data that arrives after the match in the same Read() call.
type ptyReader struct {
	r   io.Reader
	buf string
}

func (p *ptyReader) readUntil(target string) error {
	b := make([]byte, 4096)
	for {
		if idx := strings.Index(p.buf, target); idx >= 0 {
			p.buf = p.buf[idx+len(target):]
			return nil
		}
		n, err := p.r.Read(b)
		if n > 0 {
			p.buf += string(b[:n])
		}
		if err != nil {
			return err
		}
	}
}

// ptyExecCmds runs commands over a PTY shell session with realistic
// session preparation and per-command echo verification, matching the
// protocol-level behavior of Netmiko, Ansible network_cli, and Scrapli.
func ptyExecCmds(sess *ssh.Session, prompt string, cmds int) error {
	w, err := sess.StdinPipe()
	if err != nil {
		return err
	}
	r, err := sess.StdoutPipe()
	if err != nil {
		return err
	}
	if err := sess.RequestPty("vt100", 1000, 511, ssh.TerminalModes{}); err != nil {
		return err
	}
	if err := sess.Shell(); err != nil {
		return err
	}

	pr := &ptyReader{r: r}

	// Wait for initial prompt
	if err := pr.readUntil(prompt); err != nil {
		return err
	}

	// Session preparation: disable paging and set terminal width.
	// Every major tool (Netmiko, Ansible, Scrapli) does this.
	for _, prepCmd := range []string{"terminal length 0", "terminal width 511"} {
		fmt.Fprintf(w, "%s\n", prepCmd)
		if err := pr.readUntil(prepCmd); err != nil {
			return err
		}
		if err := pr.readUntil(prompt); err != nil {
			return err
		}
	}

	// Execute commands with echo verification per command
	for i := 0; i < cmds; i++ {
		cmd := "show version"
		fmt.Fprintf(w, "%s\n", cmd)
		// Phase 1: read until echoed command appears
		if err := pr.readUntil(cmd); err != nil {
			return err
		}
		// Phase 2: read until prompt (command output complete)
		if err := pr.readUntil(prompt); err != nil {
			return err
		}
	}

	fmt.Fprintf(w, "exit\n")
	return nil
}

