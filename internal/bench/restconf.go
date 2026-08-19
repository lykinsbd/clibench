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

	"github.com/lykinsbd/clibench/internal/restconfserver"
	"github.com/lykinsbd/clibench/internal/rtcount"
	"github.com/lykinsbd/clibench/internal/stats"
)

// restconfGet performs a single RESTCONF GET request.
func restconfGet(client *http.Client, addr, user, pass, cmd, accept string) error {
	path := restconfserver.CommandToPath(cmd)
	url := fmt.Sprintf("https://%s%s", addr, path)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Accept", accept)
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

// restconfPatch sends a YANG PATCH with multiple CLI commands.
func restconfPatch(client *http.Client, addr, user, pass string, cmds int) error {
	url := fmt.Sprintf("https://%s/restconf/data/", addr)
	var body strings.Builder
	for i := 0; i < cmds; i++ {
		body.WriteString("show version\n")
	}
	req, err := http.NewRequest("PATCH", url, strings.NewReader(body.String()))
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "application/yang-patch+json")
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

// RESTCONF runs all RESTCONF benchmark modes and returns the results.
func RESTCONF(c Config) []stats.Result {
	log.Printf("Benchmarking RESTCONF (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	tlsCfg := &tls.Config{InsecureSkipVerify: true}

	// Mode 1: fresh-conn — new TLS connection per iteration (JSON)
	c.pktReset()
	freshC := c.makeCounters()
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		start := time.Now()

		var cc *rtcount.Conn
		tr := &http.Transport{
			TLSClientConfig: tlsCfg,
			DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				tc, err := net.Dial(network, addr)
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
			DisableKeepAlives: true,
		}
		client := &http.Client{Transport: tr, Timeout: 30 * time.Second}

		for i := 0; i < c.Commands; i++ {
			if err := restconfGet(client, c.Addr, c.User, c.Pass, "show version", "application/yang-data+json"); err != nil {
				log.Printf("restconf fresh: %v", err)
				return errDuration
			}
		}
		tr.CloseIdleConnections()

		if cc != nil {
			freshC.recordConn(idx, cc)
		}
		c.resourceRecord(freshC, idx, snap)
		return time.Since(start)
	})

	// Mode 2: keep-alive — shared TLS connection (JSON)
	c.pktReset()
	var keepCC *rtcount.Conn
	keepTr := &http.Transport{
		TLSClientConfig: tlsCfg,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			tc, err := net.Dial(network, addr)
			if err != nil {
				return nil, err
			}
			keepCC = rtcount.Wrap(tc)
			tlsConn := tls.Client(keepCC, tlsCfg)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				tc.Close()
				return nil, err
			}
			return tlsConn, nil
		},
	}
	keepClient := &http.Client{Transport: keepTr, Timeout: 30 * time.Second}
	_ = restconfGet(keepClient, c.Addr, c.User, c.Pass, "show version", "application/yang-data+json")

	keepC := c.makeCounters()
	keepTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		var bt, br, bw int
		if c.Concurrency == 1 && keepCC != nil {
			bt, br, bw = keepCC.Trips(), keepCC.Reads(), keepCC.Writes()
		}
		start := time.Now()

		for i := 0; i < c.Commands; i++ {
			if err := restconfGet(keepClient, c.Addr, c.User, c.Pass, "show version", "application/yang-data+json"); err != nil {
				log.Printf("restconf keep-alive: %v", err)
				return errDuration
			}
		}

		if c.Concurrency == 1 && keepCC != nil {
			keepC.recordConnDelta(idx, keepCC, bt, br, bw)
		}
		c.resourceRecord(keepC, idx, snap)
		return time.Since(start)
	})
	keepTr.CloseIdleConnections()

	// Mode 3: batch-patch — YANG PATCH with all commands in one request
	c.pktReset()
	var batchCC *rtcount.Conn
	batchTr := &http.Transport{
		TLSClientConfig: tlsCfg,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			tc, err := net.Dial(network, addr)
			if err != nil {
				return nil, err
			}
			batchCC = rtcount.Wrap(tc)
			tlsConn := tls.Client(batchCC, tlsCfg)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				tc.Close()
				return nil, err
			}
			return tlsConn, nil
		},
	}
	batchClient := &http.Client{Transport: batchTr, Timeout: 30 * time.Second}
	_ = restconfGet(batchClient, c.Addr, c.User, c.Pass, "show version", "application/yang-data+json")

	batchC := c.makeCounters()
	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		var bt, br, bw int
		if c.Concurrency == 1 && batchCC != nil {
			bt, br, bw = batchCC.Trips(), batchCC.Reads(), batchCC.Writes()
		}
		start := time.Now()

		if err := restconfPatch(batchClient, c.Addr, c.User, c.Pass, c.Commands); err != nil {
			log.Printf("restconf batch: %v", err)
			return errDuration
		}

		if c.Concurrency == 1 && batchCC != nil {
			batchC.recordConnDelta(idx, batchCC, bt, br, bw)
		}
		c.resourceRecord(batchC, idx, snap)
		return time.Since(start)
	})
	batchTr.CloseIdleConnections()

	// Mode 4: json-vs-xml — keep-alive mode with XML Accept header
	c.pktReset()
	var xmlCC *rtcount.Conn
	xmlTr := &http.Transport{
		TLSClientConfig: tlsCfg,
		DialTLSContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			tc, err := net.Dial(network, addr)
			if err != nil {
				return nil, err
			}
			xmlCC = rtcount.Wrap(tc)
			tlsConn := tls.Client(xmlCC, tlsCfg)
			if err := tlsConn.HandshakeContext(ctx); err != nil {
				tc.Close()
				return nil, err
			}
			return tlsConn, nil
		},
	}
	xmlClient := &http.Client{Transport: xmlTr, Timeout: 30 * time.Second}
	_ = restconfGet(xmlClient, c.Addr, c.User, c.Pass, "show version", "application/yang-data+xml")

	xmlC := c.makeCounters()
	xmlTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		var bt, br, bw int
		if c.Concurrency == 1 && xmlCC != nil {
			bt, br, bw = xmlCC.Trips(), xmlCC.Reads(), xmlCC.Writes()
		}
		start := time.Now()

		for i := 0; i < c.Commands; i++ {
			if err := restconfGet(xmlClient, c.Addr, c.User, c.Pass, "show version", "application/yang-data+xml"); err != nil {
				log.Printf("restconf xml: %v", err)
				return errDuration
			}
		}

		if c.Concurrency == 1 && xmlCC != nil {
			xmlC.recordConnDelta(idx, xmlCC, bt, br, bw)
		}
		c.resourceRecord(xmlC, idx, snap)
		return time.Since(start)
	})
	xmlTr.CloseIdleConnections()

	return []stats.Result{
		c.summarize("restconf", "fresh-conn", freshTimes, freshC),
		c.summarize("restconf", "keep-alive", keepTimes, keepC),
		c.summarize("restconf", "batch-patch", batchTimes, batchC),
		c.summarize("restconf", "json-vs-xml", xmlTimes, xmlC),
	}
}
