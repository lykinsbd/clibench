package bench

import (
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/lykinsbd/clibench/internal/stats"
)

// HTTP3 runs all HTTP/3 benchmark modes and returns the results.
func HTTP3(c Config) []stats.Result {
	log.Printf("Benchmarking HTTP/3 (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	tlsCfg := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http3.NextProtoH3}}

	// Mode 1: fresh connection per iteration
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
		start := time.Now()
		tr := &http3.Transport{TLSClientConfig: tlsCfg.Clone()}
		client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(client, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("http3 fresh: %v", err)
				tr.Close()
				return errDuration
			}
		}
		tr.Close()
		return time.Since(start)
	})

	// Mode 2: keep-alive (shared QUIC connection)
	keepTr := &http3.Transport{TLSClientConfig: tlsCfg.Clone()}
	keepClient := &http.Client{Transport: keepTr, Timeout: 30 * time.Second}
	_ = doHTTPExec(keepClient, c.Addr, c.User, c.Pass) // warmup

	keepTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
		start := time.Now()
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(keepClient, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("http3 keep-alive: %v", err)
				return errDuration
			}
		}
		return time.Since(start)
	})
	keepTr.Close()

	// Mode 3: batch POST over shared connection
	batchPayload := stats.GenerateExecPayload(c.Commands)
	batchTr := &http3.Transport{TLSClientConfig: tlsCfg.Clone()}
	batchClient := &http.Client{Transport: batchTr, Timeout: 30 * time.Second}
	_ = doHTTPExec(batchClient, c.Addr, c.User, c.Pass) // warmup

	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
		start := time.Now()
		url := fmt.Sprintf("https://%s/admin/config", c.Addr)
		req, err := http.NewRequest("POST", url, strings.NewReader(batchPayload))
		if err != nil {
			return errDuration
		}
		req.SetBasicAuth(c.User, c.Pass)
		resp, err := batchClient.Do(req)
		if err != nil {
			log.Printf("http3 batch: %v", err)
			return errDuration
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("http3 batch: HTTP %d", resp.StatusCode)
			return errDuration
		}
		return time.Since(start)
	})
	batchTr.Close()

	results := []stats.Result{
		stats.Summarize("http3", "fresh-conn", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, freshTimes),
		stats.Summarize("http3", "keep-alive", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, keepTimes),
		stats.Summarize("http3", "batch-post", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, batchTimes),
	}

	// Mode 4: 0-RTT resumption
	// First connection stores the session ticket, second uses it for 0-RTT.
	sessionCache := tls.NewLRUClientSessionCache(1)
	zeroRTTCfg := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{http3.NextProtoH3},
		ClientSessionCache: sessionCache,
	}

	// Warmup: establish initial connection to get session ticket
	warmTr := &http3.Transport{TLSClientConfig: zeroRTTCfg}
	warmClient := &http.Client{Transport: warmTr, Timeout: 30 * time.Second}
	if err := doHTTPExec(warmClient, c.Addr, c.User, c.Pass); err != nil {
		log.Printf("http3 0rtt warmup: %v", err)
	}
	warmTr.Close()
	// Small pause to ensure session ticket is stored
	time.Sleep(50 * time.Millisecond)

	zeroTimes := stats.RunParallel(c.Iterations, c.Concurrency, func() time.Duration {
		start := time.Now()
		tr := &http3.Transport{
			TLSClientConfig: zeroRTTCfg,
			QUICConfig:      &quic.Config{Allow0RTT: true},
		}
		client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(client, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("http3 0rtt: %v", err)
				tr.Close()
				return errDuration
			}
		}
		tr.Close()
		return time.Since(start)
	})
	results = append(results, stats.Summarize("http3", "0rtt-resumption", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, zeroTimes))

	return results
}
