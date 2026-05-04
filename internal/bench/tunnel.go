package bench

import (
	"log"
	"time"

	"github.com/lykinsbd/clibench/internal/stats"
	"golang.org/x/crypto/ssh"
)

// TunnelConfig extends Config with tunnel-specific addresses.
type TunnelConfig struct {
	Config
	HTTPSHeadendAddr string // SSH addr of headend proxy using HTTPS WAN transport
	H3HeadendAddr    string // SSH addr of headend proxy using HTTP/3 WAN transport
}

// Tunnel runs all tunnel benchmark modes and returns the results.
func Tunnel(c TunnelConfig) []stats.Result {
	log.Printf("Benchmarking Tunnel (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)
	cfg := sshConfig(c.User, c.Pass)

	var results []stats.Result

	if c.HTTPSHeadendAddr != "" {
		results = append(results, tunnelFresh(cfg, c, c.HTTPSHeadendAddr, "tunnel", "ssh-https-ssh")...)
	}
	if c.H3HeadendAddr != "" {
		results = append(results, tunnelFresh(cfg, c, c.H3HeadendAddr, "tunnel", "ssh-http3-ssh")...)
	}

	return results
}

func tunnelFresh(cfg *ssh.ClientConfig, c TunnelConfig, edgeAddr, transport, op string) []stats.Result {
	batchPayload := stats.GenerateExecPayload(c.Commands)

	freshC := newCounters(c.Iterations)
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		start := time.Now()
		conn, cc, err := sshDialCounted(edgeAddr, cfg)
		if err != nil {
			log.Printf("%s fresh: %v", op, err)
			return errDuration
		}
		defer conn.Close()
		for i := 0; i < c.Commands; i++ {
			sess, err := conn.NewSession()
			if err != nil {
				log.Printf("%s session: %v", op, err)
				return errDuration
			}
			_, err = sess.Output("show version")
			sess.Close()
			if err != nil {
				log.Printf("%s exec: %v", op, err)
				return errDuration
			}
		}
		freshC.recordConn(idx, cc)
		return time.Since(start)
	})

	batchC := newCounters(c.Iterations)
	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		start := time.Now()
		conn, cc, err := sshDialCounted(edgeAddr, cfg)
		if err != nil {
			log.Printf("%s-batch: %v", op, err)
			return errDuration
		}
		defer conn.Close()
		sess, err := conn.NewSession()
		if err != nil {
			log.Printf("%s-batch session: %v", op, err)
			return errDuration
		}
		_, err = sess.Output(batchPayload)
		sess.Close()
		if err != nil {
			log.Printf("%s-batch exec: %v", op, err)
			return errDuration
		}
		batchC.recordConn(idx, cc)
		return time.Since(start)
	})

	return []stats.Result{
		stats.Summarize(transport, op, c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, freshTimes, freshC.iter()),
		stats.Summarize(transport, op+"-batch", c.Commands, c.Iterations, c.Concurrency, c.Profile, c.RTTms, batchTimes, batchC.iter()),
	}
}
