package bench

import (
	"context"
	"crypto/tls"
	"log"
	"net"
	"time"

	"github.com/lykinsbd/clibench/internal/gnmiserver"
	"github.com/lykinsbd/clibench/internal/rtcount"
	"github.com/lykinsbd/clibench/internal/stats"

	pb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// gnmiPaths returns n gNMI paths for "show version".
func gnmiPaths(n int) []*pb.Path {
	paths := make([]*pb.Path, n)
	for i := range paths {
		paths[i] = gnmiserver.CommandToPath("show version")
	}
	return paths
}

// gnmiDialOpts returns gRPC dial options with TLS and a context dialer that
// wraps the TCP connection with rtcount. The wrapped connection is stored at *pcc.
func gnmiDialOpts(pcc **rtcount.Conn) []grpc.DialOption {
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	creds := credentials.NewTLS(tlsCfg)
	return []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			d := net.Dialer{}
			tc, err := d.DialContext(ctx, "tcp", addr)
			if err != nil {
				return nil, err
			}
			cc := rtcount.Wrap(tc)
			*pcc = cc
			return cc, nil
		}),
	}
}

// GNMI runs all gNMI benchmark modes and returns the results.
func GNMI(c Config) []stats.Result {
	log.Printf("Benchmarking gNMI (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	paths := gnmiPaths(c.Commands)
	ctx := context.Background()

	// Mode 1: fresh-conn — new gRPC connection per iteration
	c.pktReset()
	freshC := c.makeCounters()
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		start := time.Now()

		var cc *rtcount.Conn
		conn, err := grpc.NewClient(c.Addr, gnmiDialOpts(&cc)...)
		if err != nil {
			log.Printf("gnmi fresh dial: %v", err)
			return errDuration
		}
		client := pb.NewGNMIClient(conn)

		_, err = client.Get(ctx, &pb.GetRequest{Path: paths, Encoding: pb.Encoding_ASCII})
		if err != nil {
			log.Printf("gnmi fresh get: %v", err)
			conn.Close()
			return errDuration
		}
		conn.Close()

		if cc != nil {
			freshC.recordConn(idx, cc)
		}
		c.resourceRecord(freshC, idx, snap)
		return time.Since(start)
	})

	// Mode 2: reuse-stream — shared gRPC connection, unary Get per command
	c.pktReset()
	var keepCC *rtcount.Conn
	keepConn, err := grpc.NewClient(c.Addr, gnmiDialOpts(&keepCC)...)
	if err != nil {
		log.Printf("gnmi reuse dial: %v", err)
		return []stats.Result{c.summarize("gnmi", "fresh-conn", freshTimes, freshC)}
	}
	keepClient := pb.NewGNMIClient(keepConn)
	// Warm up the connection.
	_, _ = keepClient.Get(ctx, &pb.GetRequest{
		Path:     []*pb.Path{gnmiserver.CommandToPath("show version")},
		Encoding: pb.Encoding_ASCII,
	})

	reuseC := c.makeCounters()
	reuseTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		var bt, br, bw int
		if c.Concurrency == 1 && keepCC != nil {
			bt, br, bw = keepCC.Trips(), keepCC.Reads(), keepCC.Writes()
		}
		start := time.Now()

		for i := 0; i < c.Commands; i++ {
			_, err := keepClient.Get(ctx, &pb.GetRequest{
				Path:     paths[i : i+1],
				Encoding: pb.Encoding_ASCII,
			})
			if err != nil {
				log.Printf("gnmi reuse get: %v", err)
				return errDuration
			}
		}

		if c.Concurrency == 1 && keepCC != nil {
			reuseC.recordConnDelta(idx, keepCC, bt, br, bw)
		}
		c.resourceRecord(reuseC, idx, snap)
		return time.Since(start)
	})
	keepConn.Close()

	// Mode 3: batch-set — all paths in one Set RPC
	c.pktReset()
	var batchCC *rtcount.Conn
	batchConn, err := grpc.NewClient(c.Addr, gnmiDialOpts(&batchCC)...)
	if err != nil {
		log.Printf("gnmi batch dial: %v", err)
		return []stats.Result{
			c.summarize("gnmi", "fresh-conn", freshTimes, freshC),
			c.summarize("gnmi", "reuse-stream", reuseTimes, reuseC),
		}
	}
	batchClient := pb.NewGNMIClient(batchConn)
	// Warm up.
	_, _ = batchClient.Get(ctx, &pb.GetRequest{
		Path:     []*pb.Path{gnmiserver.CommandToPath("show version")},
		Encoding: pb.Encoding_ASCII,
	})

	updates := make([]*pb.Update, c.Commands)
	for i := range updates {
		updates[i] = &pb.Update{
			Path: gnmiserver.CommandToPath("show version"),
			Val:  &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: ""}},
		}
	}

	batchC := c.makeCounters()
	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		var bt, br, bw int
		if c.Concurrency == 1 && batchCC != nil {
			bt, br, bw = batchCC.Trips(), batchCC.Reads(), batchCC.Writes()
		}
		start := time.Now()

		_, err := batchClient.Set(ctx, &pb.SetRequest{Update: updates})
		if err != nil {
			log.Printf("gnmi batch set: %v", err)
			return errDuration
		}

		if c.Concurrency == 1 && batchCC != nil {
			batchC.recordConnDelta(idx, batchCC, bt, br, bw)
		}
		c.resourceRecord(batchC, idx, snap)
		return time.Since(start)
	})
	batchConn.Close()

	// Mode 4: subscribe-once — ONCE subscription for all paths in a single streaming RPC
	c.pktReset()
	var subCC *rtcount.Conn
	subConn, err := grpc.NewClient(c.Addr, gnmiDialOpts(&subCC)...)
	if err != nil {
		log.Printf("gnmi subscribe dial: %v", err)
		return []stats.Result{
			c.summarize("gnmi", "fresh-conn", freshTimes, freshC),
			c.summarize("gnmi", "reuse-stream", reuseTimes, reuseC),
			c.summarize("gnmi", "batch-set", batchTimes, batchC),
		}
	}
	subClient := pb.NewGNMIClient(subConn)
	// Warm up.
	_, _ = subClient.Get(ctx, &pb.GetRequest{
		Path:     []*pb.Path{gnmiserver.CommandToPath("show version")},
		Encoding: pb.Encoding_ASCII,
	})

	subscriptions := make([]*pb.Subscription, c.Commands)
	for i := range subscriptions {
		subscriptions[i] = &pb.Subscription{Path: gnmiserver.CommandToPath("show version")}
	}

	subC := c.makeCounters()
	subTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		var bt, br, bw int
		if c.Concurrency == 1 && subCC != nil {
			bt, br, bw = subCC.Trips(), subCC.Reads(), subCC.Writes()
		}
		start := time.Now()

		stream, err := subClient.Subscribe(ctx)
		if err != nil {
			log.Printf("gnmi subscribe open: %v", err)
			return errDuration
		}

		err = stream.Send(&pb.SubscribeRequest{
			Request: &pb.SubscribeRequest_Subscribe{
				Subscribe: &pb.SubscriptionList{
					Subscription: subscriptions,
					Mode:         pb.SubscriptionList_ONCE,
				},
			},
		})
		if err != nil {
			log.Printf("gnmi subscribe send: %v", err)
			return errDuration
		}

		// Read responses until sync_response.
		for {
			resp, err := stream.Recv()
			if err != nil {
				log.Printf("gnmi subscribe recv: %v", err)
				return errDuration
			}
			if resp.GetSyncResponse() {
				break
			}
		}
		stream.CloseSend()

		if c.Concurrency == 1 && subCC != nil {
			subC.recordConnDelta(idx, subCC, bt, br, bw)
		}
		c.resourceRecord(subC, idx, snap)
		return time.Since(start)
	})
	subConn.Close()

	return []stats.Result{
		c.summarize("gnmi", "fresh-conn", freshTimes, freshC),
		c.summarize("gnmi", "reuse-stream", reuseTimes, reuseC),
		c.summarize("gnmi", "batch-set", batchTimes, batchC),
		c.summarize("gnmi", "subscribe-once", subTimes, subC),
	}
}
