// Package bench implements the SSH, HTTPS, and proxy benchmark modes.
package bench

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/lykinsbd/clibench/internal/rtcount"
	"github.com/lykinsbd/clibench/internal/stats"
	"golang.org/x/crypto/ssh"
)

var errDuration = stats.ErrDuration

// LatencyProfiles maps profile names to one-way delays.
var LatencyProfiles = map[string]time.Duration{
	"local":            0,
	"campus":           1 * time.Millisecond,
	"regional":         15 * time.Millisecond,
	"continental":      35 * time.Millisecond,
	"intercontinental": 75 * time.Millisecond,
	"transpacific":     87 * time.Millisecond,
}

// Config holds parameters for a benchmark run.
type Config struct {
	Addr        string // target server address (host:port)
	User        string // authentication username
	Pass        string // authentication password
	Iterations  int    // number of iterations per benchmark mode
	Concurrency int    // concurrent workers
	Commands    int    // commands per iteration
	Profile     string // latency profile name for result labeling
	RTTms       float64 // simulated round-trip time in milliseconds
	Hostname    string // device hostname for PTY prompt detection
}

func sshConfig(user, pass string) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         30 * time.Second,
	}
}

// sshDialCounted dials an SSH connection over a counted net.Conn.
func sshDialCounted(addr string, cfg *ssh.ClientConfig) (*ssh.Client, *rtcount.Conn, error) {
	tc, err := net.DialTimeout("tcp", addr, cfg.Timeout)
	if err != nil {
		return nil, nil, err
	}
	cc := rtcount.Wrap(tc)
	sconn, chans, reqs, err := ssh.NewClientConn(cc, addr, cfg)
	if err != nil {
		tc.Close()
		return nil, nil, err
	}
	return ssh.NewClient(sconn, chans, reqs), cc, nil
}

// SSH runs all SSH benchmark modes and returns the results.
func SSH(c Config) []stats.Result {
	log.Printf("Benchmarking SSH (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)
	cfg := sshConfig(c.User, c.Pass)

	// Mode 1: fresh connection per iteration
	freshTrips := make([]int, c.Iterations)
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		start := time.Now()
		conn, cc, err := sshDialCounted(c.Addr, cfg)
		if err != nil {
			log.Printf("ssh dial: %v", err)
			return errDuration
		}
		defer conn.Close()
		for i := 0; i < c.Commands; i++ {
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
		freshTrips[idx] = cc.Trips()
		return time.Since(start)
	})

	// Mode 2: reuse one connection (ControlMaster-style)
	sharedConn, sharedCC, err := sshDialCounted(c.Addr, cfg)
	var reuseTimes []time.Duration
	var reuseTrips []int
	if err != nil {
		log.Printf("ssh reuse dial: %v (skipping reuse test)", err)
	} else {
		if sess, err := sharedConn.NewSession(); err == nil {
			_, _ = sess.Output("show version")
			sess.Close()
		}
		baseTrips := sharedCC.Trips()
		reuseTrips = make([]int, c.Iterations)
		reuseTimes = stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
			before := sharedCC.Trips()
			start := time.Now()
			for i := 0; i < c.Commands; i++ {
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
			reuseTrips[idx] = sharedCC.Trips() - before
			return time.Since(start)
		})
		_ = baseTrips
		sharedConn.Close()
	}

	// Mode 3: batch exec
	batchPayload := stats.GenerateExecPayload(c.Commands)
	batchTrips := make([]int, c.Iterations)
	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		start := time.Now()
		conn, cc, err := sshDialCounted(c.Addr, cfg)
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
		batchTrips[idx] = cc.Trips()
		return time.Since(start)
	})

	results := []stats.Result{
		stats.Summarize("ssh", "fresh-conn", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, freshTimes, freshTrips),
	}
	if reuseTimes != nil {
		results = append(results, stats.Summarize("ssh", "reuse-conn", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, reuseTimes, reuseTrips))
	}
	results = append(results, stats.Summarize("ssh", "batch-exec", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, batchTimes, batchTrips))

	// Mode 4: PTY/shell — fresh connection per iteration
	prompt := c.Hostname + "#"
	ptyFreshTrips := make([]int, c.Iterations)
	ptyFreshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		start := time.Now()
		conn, cc, err := sshDialCounted(c.Addr, cfg)
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
		if err := ptyExecCmds(sess, prompt, c.Commands); err != nil {
			log.Printf("ssh pty-fresh: %v", err)
			return errDuration
		}
		ptyFreshTrips[idx] = cc.Trips()
		return time.Since(start)
	})
	results = append(results, stats.Summarize("ssh", "pty-fresh", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, ptyFreshTimes, ptyFreshTrips))

	// Mode 5: PTY/shell — reuse connection
	ptyConn, ptyCC, err := sshDialCounted(c.Addr, cfg)
	if err == nil {
		if sess, err := ptyConn.NewSession(); err == nil {
			_ = ptyExecCmds(sess, prompt, 1)
			sess.Close()
		}
		ptyReuseTrips := make([]int, c.Iterations)
		ptyReuseTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
			before := ptyCC.Trips()
			start := time.Now()
			sess, err := ptyConn.NewSession()
			if err != nil {
				return errDuration
			}
			defer sess.Close()
			if err := ptyExecCmds(sess, prompt, c.Commands); err != nil {
				log.Printf("ssh pty-reuse: %v", err)
				return errDuration
			}
			ptyReuseTrips[idx] = ptyCC.Trips() - before
			return time.Since(start)
		})
		ptyConn.Close()
		results = append(results, stats.Summarize("ssh", "pty-reuse", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, ptyReuseTimes, ptyReuseTrips))
	}

	return results
}

// HTTPS runs all HTTPS benchmark modes and returns the results.
func HTTPS(c Config) []stats.Result {
	log.Printf("Benchmarking HTTPS (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	tlsCfg := &tls.Config{InsecureSkipVerify: true}

	// countingTransport returns an http.Transport that wraps connections with rtcount.
	// The returned *rtcount.Conn pointer is set on first dial.
	countingTransport := func(disableKeepAlive bool) (*http.Transport, **rtcount.Conn) {
		cc := new(*rtcount.Conn)
		tr := &http.Transport{
			TLSClientConfig:   tlsCfg,
			DisableKeepAlives: disableKeepAlive,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				tc, err := net.Dial(network, addr)
				if err != nil {
					return nil, err
				}
				wrapped := rtcount.Wrap(tc)
				*cc = wrapped
				tlsConn := tls.Client(wrapped, tlsCfg)
				if err := tlsConn.HandshakeContext(ctx); err != nil {
					tc.Close()
					return nil, err
				}
				return tlsConn, nil
			},
		}
		return tr, cc
	}

	// Mode 1: fresh connection per iteration
	freshTrips := make([]int, c.Iterations)
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		start := time.Now()
		var conns []*rtcount.Conn
		tr := &http.Transport{
			TLSClientConfig:   tlsCfg,
			DisableKeepAlives: true,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				tc, err := net.Dial(network, addr)
				if err != nil {
					return nil, err
				}
				wrapped := rtcount.Wrap(tc)
				conns = append(conns, wrapped)
				tlsConn := tls.Client(wrapped, tlsCfg)
				if err := tlsConn.HandshakeContext(ctx); err != nil {
					tc.Close()
					return nil, err
				}
				return tlsConn, nil
			},
		}
		client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(client, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("https fresh: %v", err)
				return errDuration
			}
		}
		total := 0
		for _, cc := range conns {
			total += cc.Trips()
		}
		freshTrips[idx] = total
		return time.Since(start)
	})

	// Mode 2: keep-alive — shared connection, count per iteration
	keepTr, keepCC := countingTransport(false)
	keepClient := &http.Client{Transport: keepTr, Timeout: 30 * time.Second}
	_ = doHTTPExec(keepClient, c.Addr, c.User, c.Pass)

	keepTrips := make([]int, c.Iterations)
	reuseTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		var before int
		if *keepCC != nil {
			before = (*keepCC).Trips()
		}
		start := time.Now()
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(keepClient, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("https keep-alive: %v", err)
				return errDuration
			}
		}
		if *keepCC != nil {
			keepTrips[idx] = (*keepCC).Trips() - before
		}
		return time.Since(start)
	})

	// Mode 3: batch POST
	batchPayload := stats.GenerateExecPayload(c.Commands)
	batchTr, batchCC := countingTransport(false)
	batchClient := &http.Client{Transport: batchTr, Timeout: 30 * time.Second}
	_ = doHTTPExec(batchClient, c.Addr, c.User, c.Pass)

	batchTrips := make([]int, c.Iterations)
	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		var before int
		if *batchCC != nil {
			before = (*batchCC).Trips()
		}
		start := time.Now()
		url := fmt.Sprintf("https://%s/admin/config", c.Addr)
		req, err := http.NewRequest("POST", url, strings.NewReader(batchPayload))
		if err != nil {
			return errDuration
		}
		req.SetBasicAuth(c.User, c.Pass)
		resp, err := batchClient.Do(req)
		if err != nil {
			log.Printf("https batch: %v", err)
			return errDuration
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("https batch: HTTP %d", resp.StatusCode)
			return errDuration
		}
		if *batchCC != nil {
			batchTrips[idx] = (*batchCC).Trips() - before
		}
		return time.Since(start)
	})

	results := []stats.Result{
		stats.Summarize("https", "fresh-conn", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, freshTimes, freshTrips),
		stats.Summarize("https", "keep-alive", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, reuseTimes, keepTrips),
		stats.Summarize("https", "batch-post", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, batchTimes, batchTrips),
	}

	// Mode 4: multi-command GET (ASA slash syntax)
	if c.Commands > 1 {
		cmdParts := make([]string, c.Commands)
		for i := range cmdParts {
			cmdParts[i] = "show+version"
		}
		multiPath := strings.Join(cmdParts, "/")

		multiTr, multiCC := countingTransport(false)
		multiClient := &http.Client{Transport: multiTr, Timeout: 30 * time.Second}
		_ = doHTTPExec(multiClient, c.Addr, c.User, c.Pass)

		multiTrips := make([]int, c.Iterations)
		multiTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
			var before int
			if *multiCC != nil {
				before = (*multiCC).Trips()
			}
			start := time.Now()
			url := fmt.Sprintf("https://%s/admin/exec/%s", c.Addr, multiPath)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				return errDuration
			}
			req.SetBasicAuth(c.User, c.Pass)
			resp, err := multiClient.Do(req)
			if err != nil {
				log.Printf("https multi: %v", err)
				return errDuration
			}
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				log.Printf("https multi: HTTP %d", resp.StatusCode)
				return errDuration
			}
			if *multiCC != nil {
				multiTrips[idx] = (*multiCC).Trips() - before
			}
			return time.Since(start)
		})
		results = append(results, stats.Summarize("https", "multi-cmd", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, multiTimes, multiTrips))
	}

	return results
}

// ProxyConfig extends Config with proxy-specific addresses.
type ProxyConfig struct {
	Config
	FreshAddr  string // HTTPS proxy address (fresh SSH per request)
	PooledAddr string // HTTPS proxy address (pooled SSH connection)
}

// Proxy runs all proxy benchmark modes and returns the results.
func Proxy(c ProxyConfig) []stats.Result {
	log.Printf("Benchmarking Proxy (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	payload := stats.GenerateExecPayload(c.Commands)

	doProxy := func(addr string) (time.Duration, int) {
		start := time.Now()
		var cc *rtcount.Conn
		tr := &http.Transport{
			TLSClientConfig: tlsCfg,
			DialTLSContext: func(ctx context.Context, network, a string) (net.Conn, error) {
				tc, err := net.Dial(network, a)
				if err != nil {
					return nil, err
				}
				cc = rtcount.Wrap(tc)
				tlsConn := tls.Client(cc, tlsCfg)
				if err := tlsConn.HandshakeContext(ctx); err != nil {
					tc.Close()
					return nil, err
				}
				return tlsConn, nil
			},
		}
		client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
		url := fmt.Sprintf("https://%s/admin/config", addr)
		req, err := http.NewRequest("POST", url, strings.NewReader(payload))
		if err != nil {
			return errDuration, 0
		}
		req.SetBasicAuth(c.User, c.Pass)
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("proxy: %v", err)
			return errDuration, 0
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("proxy: HTTP %d", resp.StatusCode)
			return errDuration, 0
		}
		var trips int
		if cc != nil {
			trips = cc.Trips()
		}
		return time.Since(start), trips
	}

	freshTrips := make([]int, c.Iterations)
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		d, t := doProxy(c.FreshAddr)
		freshTrips[idx] = t
		return d
	})
	pooledTrips := make([]int, c.Iterations)
	pooledTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		d, t := doProxy(c.PooledAddr)
		pooledTrips[idx] = t
		return d
	})

	return []stats.Result{
		stats.Summarize("proxy", "fresh-ssh", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, freshTimes, freshTrips),
		stats.Summarize("proxy", "pooled-ssh", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, pooledTimes, pooledTrips),
	}
}

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
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
