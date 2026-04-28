// Package bench implements the SSH, HTTPS, and proxy benchmark modes.
package bench

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

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
	Addr        string // SSH or HTTPS address
	User        string
	Pass        string
	Iterations  int
	Concurrency int
	Commands    int
	Profile     string
	RTTms       float64
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

// SSH runs all SSH benchmark modes and returns the results.
func SSH(c Config) []stats.Result {
	log.Printf("Benchmarking SSH (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)
	cfg := sshConfig(c.User, c.Pass)

	// Mode 1: fresh connection per iteration
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
		start := time.Now()
		conn, err := ssh.Dial("tcp", c.Addr, cfg)
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
		return time.Since(start)
	})

	// Mode 2: reuse one connection (ControlMaster-style)
	sharedConn, err := ssh.Dial("tcp", c.Addr, cfg)
	var reuseTimes []time.Duration
	if err != nil {
		log.Printf("ssh reuse dial: %v (skipping reuse test)", err)
	} else {
		if sess, err := sharedConn.NewSession(); err == nil {
			_, _ = sess.Output("show version")
			sess.Close()
		}
		reuseTimes = stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
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
			return time.Since(start)
		})
		sharedConn.Close()
	}

	// Mode 3: batch exec
	batchPayload := stats.GenerateExecPayload(c.Commands)
	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
		start := time.Now()
		conn, err := ssh.Dial("tcp", c.Addr, cfg)
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
		stats.Summarize("ssh", "fresh-conn", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, freshTimes),
	}
	if reuseTimes != nil {
		results = append(results, stats.Summarize("ssh", "reuse-conn", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, reuseTimes))
	}
	results = append(results, stats.Summarize("ssh", "batch-exec", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, batchTimes))

	// Mode 4: PTY/shell — fresh connection per iteration
	prompt := c.Hostname + "#"
	ptyFreshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
		start := time.Now()
		conn, err := ssh.Dial("tcp", c.Addr, cfg)
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
		return time.Since(start)
	})
	results = append(results, stats.Summarize("ssh", "pty-fresh", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, ptyFreshTimes))

	// Mode 5: PTY/shell — reuse connection
	ptyConn, err := ssh.Dial("tcp", c.Addr, cfg)
	if err == nil {
		ptyReuseTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
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
			return time.Since(start)
		})
		ptyConn.Close()
		results = append(results, stats.Summarize("ssh", "pty-reuse", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, ptyReuseTimes))
	}

	return results
}

// HTTPS runs all HTTPS benchmark modes and returns the results.
func HTTPS(c Config) []stats.Result {
	log.Printf("Benchmarking HTTPS (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	tlsCfg := &tls.Config{InsecureSkipVerify: true}

	// Mode 1: fresh connection per iteration
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
		start := time.Now()
		client := &http.Client{
			Transport: &http.Transport{
				TLSClientConfig:   tlsCfg,
				DisableKeepAlives: true,
			},
			Timeout: 30 * time.Second,
		}
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(client, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("https fresh: %v", err)
				return errDuration
			}
		}
		return time.Since(start)
	})

	// Mode 2: keep-alive
	keepAliveClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: tlsCfg},
		Timeout:   30 * time.Second,
	}
	_ = doHTTPExec(keepAliveClient, c.Addr, c.User, c.Pass)

	reuseTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
		start := time.Now()
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(keepAliveClient, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("https keep-alive: %v", err)
				return errDuration
			}
		}
		return time.Since(start)
	})

	// Mode 3: batch POST
	batchPayload := stats.GenerateExecPayload(c.Commands)
	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
		start := time.Now()
		client := &http.Client{
			Transport: &http.Transport{TLSClientConfig: tlsCfg},
			Timeout:   30 * time.Second,
		}
		url := fmt.Sprintf("https://%s/admin/config", c.Addr)
		req, err := http.NewRequest("POST", url, strings.NewReader(batchPayload))
		if err != nil {
			return errDuration
		}
		req.SetBasicAuth(c.User, c.Pass)
		resp, err := client.Do(req)
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
		return time.Since(start)
	})

	results := []stats.Result{
		stats.Summarize("https", "fresh-conn", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, freshTimes),
		stats.Summarize("https", "keep-alive", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, reuseTimes),
		stats.Summarize("https", "batch-post", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, batchTimes),
	}

	// Mode 4: multi-command GET (ASA slash syntax)
	if c.Commands > 1 {
		cmdParts := make([]string, c.Commands)
		for i := range cmdParts {
			cmdParts[i] = "show+version"
		}
		multiPath := strings.Join(cmdParts, "/")

		multiTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
			start := time.Now()
			client := &http.Client{
				Transport: &http.Transport{TLSClientConfig: tlsCfg},
				Timeout:   30 * time.Second,
			}
			url := fmt.Sprintf("https://%s/admin/exec/%s", c.Addr, multiPath)
			req, err := http.NewRequest("GET", url, nil)
			if err != nil {
				return errDuration
			}
			req.SetBasicAuth(c.User, c.Pass)
			resp, err := client.Do(req)
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
			return time.Since(start)
		})
		results = append(results, stats.Summarize("https", "multi-cmd", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, multiTimes))
	}

	return results
}

// ProxyConfig extends Config with proxy-specific addresses.
type ProxyConfig struct {
	Config
	FreshAddr  string
	PooledAddr string
}

// Proxy runs all proxy benchmark modes and returns the results.
func Proxy(c ProxyConfig) []stats.Result {
	log.Printf("Benchmarking Proxy (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	payload := stats.GenerateExecPayload(c.Commands)

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
		req.SetBasicAuth(c.User, c.Pass)
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("proxy: %v", err)
			return errDuration
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("proxy: HTTP %d", resp.StatusCode)
			return errDuration
		}
		return time.Since(start)
	}

	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration { return doProxy(c.FreshAddr) })
	pooledTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration { return doProxy(c.PooledAddr) })

	return []stats.Result{
		stats.Summarize("proxy", "fresh-ssh", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, freshTimes),
		stats.Summarize("proxy", "pooled-ssh", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, pooledTimes),
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
