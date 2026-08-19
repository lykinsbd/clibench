package bench

import (
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/lykinsbd/clibench/internal/netconfserver"
	"github.com/lykinsbd/clibench/internal/rtcount"
	"github.com/lykinsbd/clibench/internal/stats"
	"golang.org/x/crypto/ssh"
)

// netconfDial opens an SSH connection and starts a NETCONF subsystem session.
// Returns an io.ReadWriteCloser for the NETCONF channel, the wrapped rtcount.Conn, and a cleanup function.
func netconfDial(addr, user, pass string) (io.ReadWriteCloser, *rtcount.Conn, func(), error) {
	cfg := &ssh.ClientConfig{
		User:            user,
		Auth:            []ssh.AuthMethod{ssh.Password(pass)},
		HostKeyCallback: netconfserver.IgnoreHostKey(),
		Timeout:         30 * time.Second,
	}

	tc, err := net.DialTimeout("tcp", addr, 30*time.Second)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial: %w", err)
	}
	cc := rtcount.Wrap(tc)
	sshConn, chans, reqs, err := ssh.NewClientConn(cc, addr, cfg)
	if err != nil {
		tc.Close()
		return nil, nil, nil, fmt.Errorf("ssh: %w", err)
	}
	go ssh.DiscardRequests(reqs)
	go func() { //nolint:errcheck
		for range chans {
		}
	}()

	client := ssh.NewClient(sshConn, chans, reqs)
	sess, err := client.NewSession()
	if err != nil {
		sshConn.Close()
		return nil, nil, nil, fmt.Errorf("session: %w", err)
	}

	w, err := sess.StdinPipe()
	if err != nil {
		sess.Close()
		sshConn.Close()
		return nil, nil, nil, err
	}
	r, err := sess.StdoutPipe()
	if err != nil {
		sess.Close()
		sshConn.Close()
		return nil, nil, nil, err
	}

	if err := sess.RequestSubsystem("netconf"); err != nil {
		sess.Close()
		sshConn.Close()
		return nil, nil, nil, fmt.Errorf("subsystem: %w", err)
	}

	// Create a combined channel-like wrapper.
	ch := &pipeChannel{r: r, w: w, sess: sess, conn: sshConn}

	// Exchange hellos.
	if err := netconfHello(ch); err != nil {
		ch.Close()
		return nil, nil, nil, fmt.Errorf("hello: %w", err)
	}

	cleanup := func() {
		// Send close-session.
		msg := netconfserver.CloseSessionRPC(999)
		netconfserver.WriteChunked(ch, msg) //nolint:errcheck // best-effort close
		ch.Close()
	}
	return ch, cc, cleanup, nil
}

// pipeChannel wraps stdin/stdout pipes + session to implement io.ReadWriteCloser.
type pipeChannel struct {
	r    interface{ Read([]byte) (int, error) }
	w    interface{ Write([]byte) (int, error) }
	sess *ssh.Session
	conn ssh.Conn
}

func (p *pipeChannel) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p *pipeChannel) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p *pipeChannel) Close() error {
	p.sess.Close()
	return p.conn.Close()
}

// netconfHello performs the hello exchange (read server hello, send client hello).
func netconfHello(rw io.ReadWriter) error {
	// Read server hello (EOM-framed).
	if _, err := netconfserver.ReadEOM(rw); err != nil {
		return err
	}
	// Send client hello (EOM-framed).
	hello := netconfserver.ClientHello()
	_, err := fmt.Fprintf(rw, "%s\n%s", hello, netconfserver.EOM)
	return err
}

// NETCONF runs all NETCONF benchmark modes and returns the results.
func NETCONF(c Config) []stats.Result {
	log.Printf("Benchmarking NETCONF (%d iterations, %d concurrency, %d cmds/iter)", c.Iterations, c.Concurrency, c.Commands)

	// Mode 1: fresh-session — new SSH + NETCONF hello per iteration
	c.pktReset()
	freshC := c.makeCounters()
	freshTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		start := time.Now()

		ch, cc, cleanup, err := netconfDial(c.Addr, c.User, c.Pass)
		if err != nil {
			log.Printf("netconf fresh dial: %v", err)
			return errDuration
		}

		for i := 0; i < c.Commands; i++ {
			msg := netconfserver.GetRPC(i+1, "show version")
			if err := netconfserver.WriteChunked(ch, msg); err != nil {
				log.Printf("netconf fresh write: %v", err)
				cleanup()
				return errDuration
			}
			if _, err := netconfserver.ReadChunked(ch); err != nil {
				log.Printf("netconf fresh read: %v", err)
				cleanup()
				return errDuration
			}
		}
		cleanup()

		if cc != nil {
			freshC.recordConn(idx, cc)
		}
		c.resourceRecord(freshC, idx, snap)
		return time.Since(start)
	})

	// Mode 2: reuse-session — shared NETCONF session, sequential <get> RPCs
	c.pktReset()
	ch, keepCC, keepCleanup, err := netconfDial(c.Addr, c.User, c.Pass)
	if err != nil {
		log.Printf("netconf reuse dial: %v", err)
		return []stats.Result{c.summarize("netconf", "fresh-session", freshTimes, freshC)}
	}

	reuseC := c.makeCounters()
	msgCounter := 1
	reuseTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		var bt, br, bw int
		if c.Concurrency == 1 && keepCC != nil {
			bt, br, bw = keepCC.Trips(), keepCC.Reads(), keepCC.Writes()
		}
		start := time.Now()

		for i := 0; i < c.Commands; i++ {
			msgCounter++
			msg := netconfserver.GetRPC(msgCounter, "show version")
			if err := netconfserver.WriteChunked(ch, msg); err != nil {
				log.Printf("netconf reuse write: %v", err)
				return errDuration
			}
			if _, err := netconfserver.ReadChunked(ch); err != nil {
				log.Printf("netconf reuse read: %v", err)
				return errDuration
			}
		}

		if c.Concurrency == 1 && keepCC != nil {
			reuseC.recordConnDelta(idx, keepCC, bt, br, bw)
		}
		c.resourceRecord(reuseC, idx, snap)
		return time.Since(start)
	})
	keepCleanup()

	// Mode 3: batch-rpc — pipeline all <get> RPCs, then read all replies
	c.pktReset()
	ch2, batchCC, batchCleanup, err := netconfDial(c.Addr, c.User, c.Pass)
	if err != nil {
		log.Printf("netconf batch dial: %v", err)
		return []stats.Result{
			c.summarize("netconf", "fresh-session", freshTimes, freshC),
			c.summarize("netconf", "reuse-session", reuseTimes, reuseC),
		}
	}

	batchC := c.makeCounters()
	batchMsgCounter := 1
	batchTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		var bt, br, bw int
		if c.Concurrency == 1 && batchCC != nil {
			bt, br, bw = batchCC.Trips(), batchCC.Reads(), batchCC.Writes()
		}
		start := time.Now()

		// Send all RPCs without waiting.
		for i := 0; i < c.Commands; i++ {
			batchMsgCounter++
			msg := netconfserver.GetRPC(batchMsgCounter, "show version")
			if err := netconfserver.WriteChunked(ch2, msg); err != nil {
				log.Printf("netconf batch write: %v", err)
				return errDuration
			}
		}
		// Read all replies.
		for i := 0; i < c.Commands; i++ {
			if _, err := netconfserver.ReadChunked(ch2); err != nil {
				log.Printf("netconf batch read: %v", err)
				return errDuration
			}
		}

		if c.Concurrency == 1 && batchCC != nil {
			batchC.recordConnDelta(idx, batchCC, bt, br, bw)
		}
		c.resourceRecord(batchC, idx, snap)
		return time.Since(start)
	})
	batchCleanup()

	// Mode 4: edit-commit — <edit-config> + <commit> sequence
	c.pktReset()
	ch3, editCC, editCleanup, err := netconfDial(c.Addr, c.User, c.Pass)
	if err != nil {
		log.Printf("netconf edit dial: %v", err)
		return []stats.Result{
			c.summarize("netconf", "fresh-session", freshTimes, freshC),
			c.summarize("netconf", "reuse-session", reuseTimes, reuseC),
			c.summarize("netconf", "batch-rpc", batchTimes, batchC),
		}
	}

	editC := c.makeCounters()
	editMsgCounter := 1
	editTimes := stats.RunParallel(c.Iterations, c.Concurrency, func(idx int) time.Duration {
		snap := c.resourceSnap()
		var bt, br, bw int
		if c.Concurrency == 1 && editCC != nil {
			bt, br, bw = editCC.Trips(), editCC.Reads(), editCC.Writes()
		}
		start := time.Now()

		// Send edit-config with all commands.
		editMsgCounter++
		cmds := ""
		for i := 0; i < c.Commands; i++ {
			if i > 0 {
				cmds += "\n"
			}
			cmds += "interface GigabitEthernet1\ndescription bench"
		}
		msg := netconfserver.EditConfigRPC(editMsgCounter, cmds)
		if err := netconfserver.WriteChunked(ch3, msg); err != nil {
			log.Printf("netconf edit write: %v", err)
			return errDuration
		}
		if _, err := netconfserver.ReadChunked(ch3); err != nil {
			log.Printf("netconf edit read: %v", err)
			return errDuration
		}

		// Send commit.
		editMsgCounter++
		commitMsg := netconfserver.CommitRPC(editMsgCounter)
		if err := netconfserver.WriteChunked(ch3, commitMsg); err != nil {
			log.Printf("netconf commit write: %v", err)
			return errDuration
		}
		if _, err := netconfserver.ReadChunked(ch3); err != nil {
			log.Printf("netconf commit read: %v", err)
			return errDuration
		}

		if c.Concurrency == 1 && editCC != nil {
			editC.recordConnDelta(idx, editCC, bt, br, bw)
		}
		c.resourceRecord(editC, idx, snap)
		return time.Since(start)
	})
	editCleanup()

	return []stats.Result{
		c.summarize("netconf", "fresh-session", freshTimes, freshC),
		c.summarize("netconf", "reuse-session", reuseTimes, reuseC),
		c.summarize("netconf", "batch-rpc", batchTimes, batchC),
		c.summarize("netconf", "edit-commit", editTimes, editC),
	}
}
