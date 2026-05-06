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

	"github.com/quic-go/quic-go/http3"
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

// PktCounter is the interface for packet counting (satisfied by *pktcount.Counter).
type PktCounter interface {
	Reset()
	Snapshot() (in, out int)
}

// Config holds parameters for a benchmark run.
type Config struct {
	Addr        string     // target server address (host:port)
	User        string     // authentication username
	Pass        string     // authentication password
	Iterations  int        // number of iterations per benchmark mode
	Concurrency int        // concurrent workers
	Commands    int        // commands per iteration
	Profile     string     // latency profile name for result labeling
	RTTms       float64    // simulated round-trip time in milliseconds
	Hostname    string     // device hostname for PTY prompt detection
	PktCounter  PktCounter // optional packet counter (nil when unavailable)
}

// pktReset resets the packet counter (no-op if nil).
func (c Config) pktReset() {
	if c.PktCounter != nil {
		c.PktCounter.Reset()
	}
}

// summarize computes stats and snapshots packet counts accumulated since last pktReset.
func (c Config) summarize(transport, op string, times []time.Duration, counts counters) stats.Result {
	r := stats.Summarize(stats.SummarizeConfig{
		Transport:   transport,
		Operation:   op,
		Commands:    c.Commands,
		Iterations:  c.Iterations,
		Concurrency: c.Concurrency,
		Profile:     c.Profile,
		RTTms:       c.RTTms,
		Times:       times,
		Counts:      counts.iter(),
	})
	if c.PktCounter != nil {
		r.PacketsIn, r.PacketsOut = c.PktCounter.Snapshot()
	}
	return r
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

// counters collects per-iteration stats from parallel slices.
type counters struct {
	trips, reads, writes []int
}

func newCounters(n int) counters {
	return counters{make([]int, n), make([]int, n), make([]int, n)}
}

func (c counters) recordConn(idx int, cc *rtcount.Conn) {
	c.trips[idx] = cc.Trips()
	c.reads[idx] = cc.Reads()
	c.writes[idx] = cc.Writes()
}

func (c counters) recordConnDelta(idx int, cc *rtcount.Conn, bt, br, bw int) {
	c.trips[idx] = cc.Trips() - bt
	c.reads[idx] = cc.Reads() - br
	c.writes[idx] = cc.Writes() - bw
}

func (c counters) recordConns(idx int, conns []*rtcount.Conn) {
	for _, cc := range conns {
		c.trips[idx] += cc.Trips()
		c.reads[idx] += cc.Reads()
		c.writes[idx] += cc.Writes()
	}
}

func (c counters) recordPacket(idx int, pc *rtcount.PacketConn) {
	c.trips[idx] = pc.Trips()
	c.reads[idx] = pc.Reads()
	c.writes[idx] = pc.Writes()
}

func (c counters) recordPacketDelta(idx int, pc *rtcount.PacketConn, bt, br, bw int) {
	c.trips[idx] = pc.Trips() - bt
	c.reads[idx] = pc.Reads() - br
	c.writes[idx] = pc.Writes() - bw
}

func (c counters) iter() stats.IterCounts {
	return stats.IterCounts{Trips: c.trips, Reads: c.reads, Writes: c.writes}
}

// sshFreshBench runs a fresh-connection SSH benchmark where each iteration
// dials, executes commands, and closes. Used by SSH fresh-conn, batch-exec,
// and tunnel modes.
func sshFreshBench(c Config, addr string, cfg *ssh.ClientConfig, execFn func(*ssh.Client) error) ([]time.Duration, counters) {
	cnt := newCounters(c.Iterations)
	times := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		start := time.Now()
		conn, cc, err := sshDialCounted(addr, cfg)
		if err != nil {
			return errDuration
		}
		defer conn.Close()
		if err := execFn(conn); err != nil {
			return errDuration
		}
		cnt.recordConn(idx, cc)
		return time.Since(start)
	})
	return times, cnt
}

// SSH runs all SSH benchmark modes and returns the results.
func SSH(c Config) []stats.Result {
	log.Printf("Benchmarking SSH (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)
	cfg := sshConfig(c.User, c.Pass)

	c.pktReset()
	// Mode 1: fresh connection per iteration
	freshTimes, freshC := sshFreshBench(c, c.Addr, cfg, func(conn *ssh.Client) error {
		for i := 0; i < c.Commands; i++ {
			sess, err := conn.NewSession()
			if err != nil {
				return err
			}
			_, err = sess.Output("show version")
			sess.Close()
			if err != nil {
				return err
			}
		}
		return nil
	})

	c.pktReset()
	// Mode 2: reuse one connection (ControlMaster-style)
	sharedConn, sharedCC, err := sshDialCounted(c.Addr, cfg)
	var reuseTimes []time.Duration
	var reuseC counters
	if err != nil {
		log.Printf("ssh reuse dial: %v (skipping reuse test)", err)
	} else {
		if sess, err := sharedConn.NewSession(); err == nil {
			_, _ = sess.Output("show version")
			sess.Close()
		}
		reuseC = newCounters(c.Iterations)
		reuseTimes = stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
			// Delta counting on shared conn is only valid at concurrency=1.
			var bt, br, bw int
			if c.Concurrency == 1 {
				bt, br, bw = sharedCC.Trips(), sharedCC.Reads(), sharedCC.Writes()
			}
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
			if c.Concurrency == 1 {
				reuseC.recordConnDelta(idx, sharedCC, bt, br, bw)
			}
			return time.Since(start)
		})
		sharedConn.Close()
	}

	c.pktReset()
	// Mode 3: batch exec
	batchPayload := stats.GenerateExecPayload(c.Commands)
	batchTimes, batchC := sshFreshBench(c, c.Addr, cfg, func(conn *ssh.Client) error {
		sess, err := conn.NewSession()
		if err != nil {
			return err
		}
		_, err = sess.Output(batchPayload)
		sess.Close()
		return err
	})

	results := []stats.Result{
		c.summarize("ssh", "fresh-conn", freshTimes, freshC),
	}
	if reuseTimes != nil {
		results = append(results, c.summarize("ssh", "reuse-conn", reuseTimes, reuseC))
	}
	results = append(results, c.summarize("ssh", "batch-exec", batchTimes, batchC))

	c.pktReset()
	// Mode 4: PTY/shell — fresh connection per iteration
	prompt := c.Hostname + "#"
	ptyFreshTimes, ptyFreshC := sshFreshBench(c, c.Addr, cfg, func(conn *ssh.Client) error {
		sess, err := conn.NewSession()
		if err != nil {
			return err
		}
		defer sess.Close()
		return ptyExecCmds(sess, prompt, c.Commands)
	})
	results = append(results, c.summarize("ssh", "pty-fresh", ptyFreshTimes, ptyFreshC))

	c.pktReset()
	// Mode 5: PTY/shell — reuse connection
	ptyConn, ptyCC, err := sshDialCounted(c.Addr, cfg)
	if err == nil {
		if sess, err := ptyConn.NewSession(); err == nil {
			_ = ptyExecCmds(sess, prompt, 1)
			sess.Close()
		}
		ptyReuseC := newCounters(c.Iterations)
		ptyReuseTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
			var bt, br, bw int
			if c.Concurrency == 1 {
				bt, br, bw = ptyCC.Trips(), ptyCC.Reads(), ptyCC.Writes()
			}
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
			if c.Concurrency == 1 {
				ptyReuseC.recordConnDelta(idx, ptyCC, bt, br, bw)
			}
			return time.Since(start)
		})
		ptyConn.Close()
		results = append(results, c.summarize("ssh", "pty-reuse", ptyReuseTimes, ptyReuseC))
	}

	return results
}

// HTTPS runs all HTTPS benchmark modes and returns the results.
func HTTPS(c Config) []stats.Result {
	log.Printf("Benchmarking HTTPS (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	tlsCfg := &tls.Config{InsecureSkipVerify: true}

	// countedTr wraps an http.Transport with the rtcount.Conn it created.
	type countedTr struct {
		*http.Transport
		cc *rtcount.Conn
	}

	// countingTransport returns an http.Transport that wraps connections with rtcount.
	countingTransport := func() *countedTr {
		ct := &countedTr{}
		ct.Transport = &http.Transport{
			TLSClientConfig: tlsCfg,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				tc, err := net.Dial(network, addr)
				if err != nil {
					return nil, err
				}
				ct.cc = rtcount.Wrap(tc)
				tlsConn := tls.Client(ct.cc, tlsCfg)
				if err := tlsConn.HandshakeContext(ctx); err != nil {
					tc.Close()
					return nil, err
				}
				return tlsConn, nil
			},
		}
		return ct
	}

	c.pktReset()
	// Mode 1: fresh connection per iteration
	freshC := newCounters(c.Iterations)
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
		freshC.recordConns(idx, conns)
		return time.Since(start)
	})

	c.pktReset()
	// Mode 2: keep-alive — shared connection, count per iteration
	keepTr := countingTransport()
	keepClient := &http.Client{Transport: keepTr.Transport, Timeout: 30 * time.Second}
	_ = doHTTPExec(keepClient, c.Addr, c.User, c.Pass)

	keepC := newCounters(c.Iterations)
	reuseTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		var bt, br, bw int
		if c.Concurrency == 1 && keepTr.cc != nil {
			bt, br, bw = keepTr.cc.Trips(), keepTr.cc.Reads(), keepTr.cc.Writes()
		}
		start := time.Now()
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(keepClient, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("https keep-alive: %v", err)
				return errDuration
			}
		}
		if c.Concurrency == 1 && keepTr.cc != nil {
			keepC.recordConnDelta(idx, keepTr.cc, bt, br, bw)
		}
		return time.Since(start)
	})

	c.pktReset()
	// Mode 3: batch POST
	batchPayload := stats.GenerateExecPayload(c.Commands)
	batchTr := countingTransport()
	batchClient := &http.Client{Transport: batchTr.Transport, Timeout: 30 * time.Second}
	_ = doHTTPExec(batchClient, c.Addr, c.User, c.Pass)

	batchC := newCounters(c.Iterations)
	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		var bt, br, bw int
		if c.Concurrency == 1 && batchTr.cc != nil {
			bt, br, bw = batchTr.cc.Trips(), batchTr.cc.Reads(), batchTr.cc.Writes()
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
		if c.Concurrency == 1 && batchTr.cc != nil {
			batchC.recordConnDelta(idx, batchTr.cc, bt, br, bw)
		}
		return time.Since(start)
	})

	results := []stats.Result{
		c.summarize("https", "fresh-conn", freshTimes, freshC),
		c.summarize("https", "keep-alive", reuseTimes, keepC),
		c.summarize("https", "batch-post", batchTimes, batchC),
	}

	c.pktReset()
	// Mode 4: multi-command GET (ASA slash syntax)
	if c.Commands > 1 {
		cmdParts := make([]string, c.Commands)
		for i := range cmdParts {
			cmdParts[i] = "show+version"
		}
		multiPath := strings.Join(cmdParts, "/")

		multiTr := countingTransport()
		multiClient := &http.Client{Transport: multiTr.Transport, Timeout: 30 * time.Second}
		_ = doHTTPExec(multiClient, c.Addr, c.User, c.Pass)

		multiC := newCounters(c.Iterations)
		multiTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
			var bt, br, bw int
			if c.Concurrency == 1 && multiTr.cc != nil {
				bt, br, bw = multiTr.cc.Trips(), multiTr.cc.Reads(), multiTr.cc.Writes()
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
			if c.Concurrency == 1 && multiTr.cc != nil {
				multiC.recordConnDelta(idx, multiTr.cc, bt, br, bw)
			}
			return time.Since(start)
		})
		results = append(results, c.summarize("https", "multi-cmd", multiTimes, multiC))
	}

	return results
}

// ProxyConfig extends Config with proxy-specific addresses.
type ProxyConfig struct {
	Config
	FreshAddr     string // HTTPS proxy address (fresh SSH per request)
	PooledAddr    string // HTTPS proxy address (pooled SSH connection)
	H3FreshAddr   string // HTTP/3 proxy address (fresh SSH per request)
	H3PooledAddr  string // HTTP/3 proxy address (pooled SSH connection)
}

// Proxy runs all proxy benchmark modes and returns the results.
func Proxy(c ProxyConfig) []stats.Result {
	log.Printf("Benchmarking Proxy (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	payload := stats.GenerateExecPayload(c.Commands)

	doProxy := func(addr string) (time.Duration, int, int, int) {
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
			return errDuration, 0, 0, 0
		}
		req.SetBasicAuth(c.User, c.Pass)
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("proxy: %v", err)
			return errDuration, 0, 0, 0
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("proxy: HTTP %d", resp.StatusCode)
			return errDuration, 0, 0, 0
		}
		var trips, reads, writes int
		if cc != nil {
			trips, reads, writes = cc.Trips(), cc.Reads(), cc.Writes()
		}
		return time.Since(start), trips, reads, writes
	}

	c.pktReset()
	freshC := newCounters(c.Iterations)
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		d, t, r, w := doProxy(c.FreshAddr)
		freshC.trips[idx], freshC.reads[idx], freshC.writes[idx] = t, r, w
		return d
	})
	freshResult := c.summarize("proxy", "fresh-ssh", freshTimes, freshC)

	c.pktReset()
	pooledC := newCounters(c.Iterations)
	pooledTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		d, t, r, w := doProxy(c.PooledAddr)
		pooledC.trips[idx], pooledC.reads[idx], pooledC.writes[idx] = t, r, w
		return d
	})

	results := []stats.Result{
		freshResult,
		c.summarize("proxy", "pooled-ssh", pooledTimes, pooledC),
	}

	// H3 proxy modes
	if c.H3FreshAddr != "" {
		h3TlsCfg := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h3"}}

		doProxyH3 := func(addr string) (time.Duration, int, int, int) {
			start := time.Now()
			var pc *rtcount.PacketConn
			tr := &http3.Transport{TLSClientConfig: h3TlsCfg.Clone(), Dial: h3Dial(&pc)}
			client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
			url := fmt.Sprintf("https://%s/admin/config", addr)
			req, err := http.NewRequest("POST", url, strings.NewReader(payload))
			if err != nil {
				return errDuration, 0, 0, 0
			}
			req.SetBasicAuth(c.User, c.Pass)
			resp, err := client.Do(req)
			if err != nil {
				log.Printf("proxy h3: %v", err)
				tr.Close()
				return errDuration, 0, 0, 0
			}
			_, _ = io.ReadAll(resp.Body)
			resp.Body.Close()
			tr.Close()
			if resp.StatusCode != http.StatusOK {
				log.Printf("proxy h3: HTTP %d", resp.StatusCode)
				return errDuration, 0, 0, 0
			}
			var trips, reads, writes int
			if pc != nil {
				trips, reads, writes = pc.Trips(), pc.Reads(), pc.Writes()
			}
			return time.Since(start), trips, reads, writes
		}

		c.pktReset()
		h3FreshC := newCounters(c.Iterations)
		h3FreshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
			d, t, r, w := doProxyH3(c.H3FreshAddr)
			h3FreshC.trips[idx], h3FreshC.reads[idx], h3FreshC.writes[idx] = t, r, w
			return d
		})
		results = append(results, c.summarize("proxy", "h3-fresh-ssh", h3FreshTimes, h3FreshC))

		c.pktReset()
		h3PooledC := newCounters(c.Iterations)
		h3PooledTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
			d, t, r, w := doProxyH3(c.H3PooledAddr)
			h3PooledC.trips[idx], h3PooledC.reads[idx], h3PooledC.writes[idx] = t, r, w
			return d
		})
		results = append(results, c.summarize("proxy", "h3-pooled-ssh", h3PooledTimes, h3PooledC))
	}

	return results
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
