package netconfserver_test

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/netconfserver"
	"golang.org/x/crypto/ssh"
)

func startTestServer(t *testing.T) (addr string, cleanup func()) {
	t.Helper()
	dev, err := device.New("test-rtr", "admin", "admin", "../../transcripts")
	if err != nil {
		t.Fatalf("device.New: %v", err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv, err := netconfserver.New(ln.Addr().String(), dev)
	if err != nil {
		ln.Close()
		t.Fatalf("netconfserver.New: %v", err)
	}
	srv.SetListener(ln)
	go srv.ListenAndServe()

	// Wait for server.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", ln.Addr().String(), 500*time.Millisecond)
		if err == nil {
			c.Close()
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	return ln.Addr().String(), func() { srv.Close() }
}

// dialNETCONF connects via SSH subsystem and exchanges hellos.
func dialNETCONF(t *testing.T, addr string) (io.ReadWriteCloser, func()) {
	t.Helper()
	cfg := &ssh.ClientConfig{
		User:            "admin",
		Auth:            []ssh.AuthMethod{ssh.Password("admin")},
		HostKeyCallback: netconfserver.IgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	client, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		t.Fatalf("ssh dial: %v", err)
	}
	sess, err := client.NewSession()
	if err != nil {
		client.Close()
		t.Fatalf("session: %v", err)
	}
	w, _ := sess.StdinPipe()
	r, _ := sess.StdoutPipe()
	if err := sess.RequestSubsystem("netconf"); err != nil {
		sess.Close()
		client.Close()
		t.Fatalf("subsystem: %v", err)
	}

	rw := &rwc{r: r, w: w, close: func() { sess.Close(); client.Close() }}

	// Read server hello.
	if _, err := netconfserver.ReadEOM(rw); err != nil {
		rw.Close()
		t.Fatalf("read hello: %v", err)
	}
	// Send client hello.
	fmt.Fprintf(rw, "%s\n%s", netconfserver.ClientHello(), netconfserver.EOM)

	return rw, func() { rw.Close() }
}

type rwc struct {
	r     interface{ Read([]byte) (int, error) }
	w     interface{ Write([]byte) (int, error) }
	close func()
}

func (c *rwc) Read(b []byte) (int, error)  { return c.r.Read(b) }
func (c *rwc) Write(b []byte) (int, error) { return c.w.Write(b) }
func (c *rwc) Close() error                { c.close(); return nil }

func TestGetRPC(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	rw, done := dialNETCONF(t, addr)
	defer done()

	msg := netconfserver.GetRPC(1, "show version")
	if err := netconfserver.WriteChunked(rw, msg); err != nil {
		t.Fatalf("write: %v", err)
	}
	reply, err := netconfserver.ReadChunked(rw)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(reply) < 50 {
		t.Errorf("reply too short (%d bytes): %s", len(reply), reply)
	}
	if !contains(reply, "rpc-reply") {
		t.Errorf("expected rpc-reply in response: %s", reply)
	}
	if !contains(reply, "data") {
		t.Errorf("expected <data> in response: %s", reply)
	}
}

func TestEditConfigAndCommit(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	rw, done := dialNETCONF(t, addr)
	defer done()

	// edit-config
	msg := netconfserver.EditConfigRPC(1, "interface GigabitEthernet1\ndescription test")
	if err := netconfserver.WriteChunked(rw, msg); err != nil {
		t.Fatalf("write edit: %v", err)
	}
	reply, err := netconfserver.ReadChunked(rw)
	if err != nil {
		t.Fatalf("read edit: %v", err)
	}
	if !contains(reply, "<ok/>") {
		t.Errorf("expected <ok/> in edit reply: %s", reply)
	}

	// commit
	msg = netconfserver.CommitRPC(2)
	if err := netconfserver.WriteChunked(rw, msg); err != nil {
		t.Fatalf("write commit: %v", err)
	}
	reply, err = netconfserver.ReadChunked(rw)
	if err != nil {
		t.Fatalf("read commit: %v", err)
	}
	if !contains(reply, "<ok/>") {
		t.Errorf("expected <ok/> in commit reply: %s", reply)
	}
}

func TestMultipleRPCs(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	rw, done := dialNETCONF(t, addr)
	defer done()

	for i := 1; i <= 5; i++ {
		msg := netconfserver.GetRPC(i, "show ip route")
		if err := netconfserver.WriteChunked(rw, msg); err != nil {
			t.Fatalf("write[%d]: %v", i, err)
		}
		reply, err := netconfserver.ReadChunked(rw)
		if err != nil {
			t.Fatalf("read[%d]: %v", i, err)
		}
		if !contains(reply, "rpc-reply") {
			t.Errorf("rpc[%d]: expected rpc-reply: %s", i, reply)
		}
	}
}

func contains(b []byte, s string) bool {
	return len(b) > 0 && len(s) > 0 && bytesContains(b, s)
}

func bytesContains(b []byte, s string) bool {
	return len(b) >= len(s) && (string(b) != "" && stringContains(string(b), s))
}

func stringContains(haystack, needle string) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
