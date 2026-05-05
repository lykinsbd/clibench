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

	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"

	"github.com/lykinsbd/clibench/internal/rtcount"
	"github.com/lykinsbd/clibench/internal/stats"
)

// h3Dial returns an http3.Transport.Dial function that wraps the UDP conn
// with rtcount and stores the counter at *pcc.
func h3Dial(pcc **rtcount.PacketConn) func(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
	return func(ctx context.Context, addr string, tlsCfg *tls.Config, cfg *quic.Config) (*quic.Conn, error) {
		udpAddr, err := net.ResolveUDPAddr("udp", addr)
		if err != nil {
			return nil, err
		}
		udpConn, err := net.ListenUDP("udp", nil)
		if err != nil {
			return nil, err
		}
		cc := rtcount.WrapPacket(udpConn)
		*pcc = cc
		return quic.Dial(ctx, cc, udpAddr, tlsCfg, cfg)
	}
}

// HTTP3 runs all HTTP/3 benchmark modes and returns the results.
func HTTP3(c Config) []stats.Result {
	log.Printf("Benchmarking HTTP/3 (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	tlsCfg := &tls.Config{InsecureSkipVerify: true, NextProtos: []string{http3.NextProtoH3}}

	// Mode 1: fresh connection per iteration
	freshC := newCounters(c.Iterations)
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		start := time.Now()
		var cc *rtcount.PacketConn
		tr := &http3.Transport{TLSClientConfig: tlsCfg.Clone(), Dial: h3Dial(&cc)}
		client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(client, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("http3 fresh: %v", err)
				tr.Close()
				return errDuration
			}
		}
		if cc != nil {
			freshC.recordPacket(idx, cc)
		}
		tr.Close()
		return time.Since(start)
	})

	// Mode 2: keep-alive (shared QUIC connection)
	var keepCC *rtcount.PacketConn
	keepTr := &http3.Transport{TLSClientConfig: tlsCfg.Clone(), Dial: h3Dial(&keepCC)}
	keepClient := &http.Client{Transport: keepTr, Timeout: 30 * time.Second}
	_ = doHTTPExec(keepClient, c.Addr, c.User, c.Pass)

	keepC := newCounters(c.Iterations)
	keepTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		var bt, br, bw int
		if c.Concurrency == 1 && keepCC != nil {
			bt, br, bw = keepCC.Trips(), keepCC.Reads(), keepCC.Writes()
		}
		start := time.Now()
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(keepClient, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("http3 keep-alive: %v", err)
				return errDuration
			}
		}
		if c.Concurrency == 1 && keepCC != nil {
			keepC.recordPacketDelta(idx, keepCC, bt, br, bw)
		}
		return time.Since(start)
	})
	keepTr.Close()

	// Mode 3: batch POST over shared connection
	batchPayload := stats.GenerateExecPayload(c.Commands)
	var batchCC *rtcount.PacketConn
	batchTr := &http3.Transport{TLSClientConfig: tlsCfg.Clone(), Dial: h3Dial(&batchCC)}
	batchClient := &http.Client{Transport: batchTr, Timeout: 30 * time.Second}
	_ = doHTTPExec(batchClient, c.Addr, c.User, c.Pass)

	batchC := newCounters(c.Iterations)
	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		var bt, br, bw int
		if c.Concurrency == 1 && batchCC != nil {
			bt, br, bw = batchCC.Trips(), batchCC.Reads(), batchCC.Writes()
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
			log.Printf("http3 batch: %v", err)
			return errDuration
		}
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Printf("http3 batch: HTTP %d", resp.StatusCode)
			return errDuration
		}
		if c.Concurrency == 1 && batchCC != nil {
			batchC.recordPacketDelta(idx, batchCC, bt, br, bw)
		}
		return time.Since(start)
	})
	batchTr.Close()

	results := []stats.Result{
		c.summarize("http3", "fresh-conn", freshTimes, freshC),
		c.summarize("http3", "keep-alive", keepTimes, keepC),
		c.summarize("http3", "batch-post", batchTimes, batchC),
	}

	// Mode 4: 0-RTT resumption
	sessionCache := tls.NewLRUClientSessionCache(1)
	zeroRTTCfg := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{http3.NextProtoH3},
		ClientSessionCache: sessionCache,
	}

	warmTr := &http3.Transport{TLSClientConfig: zeroRTTCfg}
	warmClient := &http.Client{Transport: warmTr, Timeout: 30 * time.Second}
	if err := doHTTPExec(warmClient, c.Addr, c.User, c.Pass); err != nil {
		log.Printf("http3 0rtt warmup: %v", err)
	}
	warmTr.Close()
	time.Sleep(50 * time.Millisecond)

	zeroC := newCounters(c.Iterations)
	zeroTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		start := time.Now()
		var cc *rtcount.PacketConn
		tr := &http3.Transport{
			TLSClientConfig: zeroRTTCfg,
			QUICConfig:      &quic.Config{Allow0RTT: true},
			Dial:            h3Dial(&cc),
		}
		client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
		for i := 0; i < c.Commands; i++ {
			if err := doHTTPExec(client, c.Addr, c.User, c.Pass); err != nil {
				log.Printf("http3 0rtt: %v", err)
				tr.Close()
				return errDuration
			}
		}
		if cc != nil {
			zeroC.recordPacket(idx, cc)
		}
		tr.Close()
		return time.Since(start)
	})
	results = append(results, c.summarize("http3", "0rtt-resumption", zeroTimes, zeroC))

	return results
}
