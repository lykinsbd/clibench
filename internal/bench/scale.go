// Package bench — concurrency sweep (issue #49).
//
// Unlike the per-iteration benchmarks (SSH/HTTPS/gNMI), which dial-exec-close
// inside each RunParallel worker, the sweep measures the cost of *holding* many
// connections open at once — the real shape of fleet automation, where a tool
// manages hundreds or thousands of devices simultaneously. For each concurrency
// level N it:
//
//  1. opens N connections/sessions simultaneously (barrier-synced), timing setup;
//  2. holds them all open and executes commands across all N in parallel;
//  3. records per-connection latency (p50/p95), wall-clock, ops/sec, and the
//     live heap held while all N sessions are alive.
package bench

import (
	"context"
	"crypto/tls"
	"log"
	"net/http"
	"runtime"
	"sync"
	"time"

	"github.com/lykinsbd/clibench/internal/resource"
	"github.com/lykinsbd/clibench/internal/rtcount"
	"github.com/lykinsbd/clibench/internal/stats"

	"github.com/lykinsbd/clibench/internal/gnmiserver"
	pb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
)

// conn is one held connection in a sweep, with the hooks the sweep needs:
// exec runs the per-op workload, close releases it.
type sweepConn struct {
	exec  func() error
	close func()
}

// opener dials one held connection for a transport, or returns an error.
type opener func(c Config) (*sweepConn, error)

// sweepOpeners maps a transport name to its hold-open dialer. Only the
// transports with the most interesting pooling/multiplexing behaviour are
// included in the first cut (issue #49); others are a follow-up.
func sweepOpeners() map[string]opener {
	return map[string]opener{
		"ssh":   openSSH,
		"https": openHTTPS,
		"gnmi":  openGNMI,
	}
}

func openSSH(c Config) (*sweepConn, error) {
	cfg := sshConfig(c.User, c.Pass)
	conn, _, err := sshDialCounted(c.Addr, cfg)
	if err != nil {
		return nil, err
	}
	return &sweepConn{
		exec: func() error {
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
		},
		close: func() { _ = conn.Close() },
	}, nil
}

func openHTTPS(c Config) (*sweepConn, error) {
	tlsCfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // benchmark server uses a self-signed cert
	// One keep-alive transport per held "device": a dedicated pooled HTTPS
	// connection, mirroring a per-device keep-alive pool.
	tr := &http.Transport{TLSClientConfig: tlsCfg, MaxIdleConnsPerHost: 1}
	client := &http.Client{Transport: tr, Timeout: 30 * time.Second}
	// Warm the connection so setup timing reflects a live, usable session.
	if err := doHTTPExec(client, c.Addr, c.User, c.Pass); err != nil {
		tr.CloseIdleConnections()
		return nil, err
	}
	return &sweepConn{
		exec: func() error {
			for i := 0; i < c.Commands; i++ {
				if err := doHTTPExec(client, c.Addr, c.User, c.Pass); err != nil {
					return err
				}
			}
			return nil
		},
		close: func() { tr.CloseIdleConnections() },
	}, nil
}

func openGNMI(c Config) (*sweepConn, error) {
	ctx := context.Background()
	var cc *rtcount.Conn
	conn, err := grpc.NewClient(c.Addr, gnmiDialOpts(&cc)...)
	if err != nil {
		return nil, err
	}
	client := pb.NewGNMIClient(conn)
	paths := gnmiPaths(c.Commands)
	// Warm up so the HTTP/2 connection is established before setup timing stops.
	if _, err := client.Get(ctx, &pb.GetRequest{
		Path:     []*pb.Path{gnmiserver.CommandToPath("show version")},
		Encoding: pb.Encoding_ASCII,
	}); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return &sweepConn{
		exec: func() error {
			_, err := client.Get(ctx, &pb.GetRequest{Path: paths, Encoding: pb.Encoding_ASCII})
			return err
		},
		close: func() { _ = conn.Close() },
	}, nil
}

// Sweep runs the concurrency sweep for the given transport across every level
// in c-derived levels, returning one stats.Result per (transport, level).
func Sweep(transport string, c Config, levels []int) []stats.Result {
	open, ok := sweepOpeners()[transport]
	if !ok {
		return nil
	}
	results := make([]stats.Result, 0, len(levels))
	for _, n := range levels {
		if n < 1 {
			continue
		}
		results = append(results, sweepLevel(transport, c, n, open))
	}
	return results
}

// sweepLevel opens n connections, holds them open, runs the workload across all
// of them in parallel, and summarizes.
func sweepLevel(transport string, c Config, n int, open opener) stats.Result {
	log.Printf("Sweep %s: opening %d simultaneous connections (%d cmds each)", transport, n, c.Commands)
	c.pktReset()

	// Phase 1 — open all N connections, timing total setup.
	runtime.GC() // establish a clean live-heap baseline before holding sessions
	baseHeap := resource.LiveHeap()
	setupStart := time.Now()
	conns := make([]*sweepConn, 0, n)
	var mu sync.Mutex
	var wg sync.WaitGroup
	errs := 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sc, err := open(c)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs++
				return
			}
			conns = append(conns, sc)
		}()
	}
	wg.Wait()
	setupMs := float64(time.Since(setupStart).Microseconds()) / 1000

	// Measure live heap while all N sessions are held open. Compare in uint64
	// (both are HeapInuse readings) and only convert the non-negative delta —
	// GC between the baseline and here can make peak < base, which we floor to 0.
	peakHeap := resource.LiveHeap()
	var liveHeapMB float64
	if peakHeap > baseHeap {
		liveHeapMB = float64(peakHeap-baseHeap) / (1024 * 1024)
	}

	defer func() {
		for _, sc := range conns {
			sc.close()
		}
	}()

	if len(conns) == 0 {
		r := c.summarize(transport, "sweep", []time.Duration{errDuration}, c.makeCounters())
		r.SweepN = n
		r.SetupMs = setupMs
		return r
	}

	// Phase 2 — barrier-synced: every held connection fires its workload at
	// once, so per-connection latency reflects contention at concurrency N.
	start := make(chan struct{})
	perConn := make([]time.Duration, len(conns))
	wallStart := time.Now()
	for i, sc := range conns {
		wg.Add(1)
		go func(idx int, sc *sweepConn) {
			defer wg.Done()
			<-start // barrier
			t0 := time.Now()
			if err := sc.exec(); err != nil {
				perConn[idx] = errDuration
				return
			}
			perConn[idx] = time.Since(t0)
		}(i, sc)
	}
	close(start) // release the barrier
	wg.Wait()
	wallMs := float64(time.Since(wallStart).Microseconds()) / 1000

	// Summarize per-connection latencies as the iteration distribution.
	cnt := c.makeCounters()
	sc := stats.SummarizeConfig{
		Transport:   transport,
		Operation:   "sweep",
		Commands:    c.Commands,
		Iterations:  len(conns),
		Concurrency: n,
		Profile:     c.Profile,
		RTTms:       c.RTTms,
		JitterMs:    c.JitterMs,
		LossPct:     c.LossPct,
		Times:       perConn,
		Counts:      cnt.iter(),
	}
	r := stats.Summarize(sc)
	if c.PktCounter != nil {
		r.PacketsIn, r.PacketsOut = c.PktCounter.Snapshot()
	}

	// Ops = successful connections * commands each.
	ops := (len(conns) - r.Errors) * c.Commands
	r.SweepN = n
	r.SetupMs = setupMs
	r.WallMs = wallMs
	r.LiveHeapMB = liveHeapMB
	if wallMs > 0 {
		r.OpsPerSec = float64(ops) / (wallMs / 1000)
	}
	return r
}
