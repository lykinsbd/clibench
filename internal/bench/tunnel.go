package bench

import (
	"log"

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
		results = append(results, tunnelModes(c, cfg, c.HTTPSHeadendAddr, "ssh-https-ssh")...)
	}
	if c.H3HeadendAddr != "" {
		results = append(results, tunnelModes(c, cfg, c.H3HeadendAddr, "ssh-http3-ssh")...)
	}
	return results
}

func tunnelModes(c TunnelConfig, cfg *ssh.ClientConfig, addr, op string) []stats.Result {
	batchPayload := stats.GenerateExecPayload(c.Commands)

	c.pktReset()
	freshTimes, freshC := sshFreshBench(c.Config, addr, cfg, func(conn *ssh.Client) error {
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

	freshResult := c.summarize("tunnel", op, freshTimes, freshC)

	c.pktReset()
	batchTimes, batchC := sshFreshBench(c.Config, addr, cfg, func(conn *ssh.Client) error {
		sess, err := conn.NewSession()
		if err != nil {
			return err
		}
		_, err = sess.Output(batchPayload)
		sess.Close()
		return err
	})

	return []stats.Result{
		freshResult,
		c.summarize("tunnel", op+"-batch", batchTimes, batchC),
	}
}
