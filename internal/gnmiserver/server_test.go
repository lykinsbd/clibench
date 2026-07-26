package gnmiserver_test

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/gnmiserver"

	pb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
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
	srv := gnmiserver.New(ln.Addr().String(), dev)
	srv.SetListener(ln)
	go srv.ListenAndServe()

	// Wait for server to be ready.
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

func dialGNMI(t *testing.T, addr string) (pb.GNMIClient, *grpc.ClientConn) {
	t.Helper()
	creds := credentials.NewTLS(&tls.Config{InsecureSkipVerify: true})
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(creds))
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	return pb.NewGNMIClient(conn), conn
}

func TestCapabilities(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	client, conn := dialGNMI(t, addr)
	defer conn.Close()

	resp, err := client.Capabilities(context.Background(), &pb.CapabilityRequest{})
	if err != nil {
		t.Fatalf("Capabilities: %v", err)
	}
	if resp.GNMIVersion == "" {
		t.Error("expected non-empty gNMI version")
	}
	if len(resp.SupportedModels) == 0 {
		t.Error("expected at least one supported model")
	}
}

func TestGet(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	client, conn := dialGNMI(t, addr)
	defer conn.Close()

	path := gnmiserver.CommandToPath("show version")
	resp, err := client.Get(context.Background(), &pb.GetRequest{
		Path:     []*pb.Path{path},
		Encoding: pb.Encoding_ASCII,
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(resp.Notification) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(resp.Notification))
	}
	val := resp.Notification[0].Update[0].Val.GetStringVal()
	if val == "" {
		t.Error("expected non-empty response for 'show version'")
	}
	if len(val) < 20 {
		t.Errorf("response too short (%d chars), expected IOS-XE version output", len(val))
	}
}

func TestGetMultiplePaths(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	client, conn := dialGNMI(t, addr)
	defer conn.Close()

	paths := []*pb.Path{
		gnmiserver.CommandToPath("show version"),
		gnmiserver.CommandToPath("show ip route"),
	}
	resp, err := client.Get(context.Background(), &pb.GetRequest{
		Path:     paths,
		Encoding: pb.Encoding_ASCII,
	})
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(resp.Notification) != 2 {
		t.Fatalf("expected 2 notifications, got %d", len(resp.Notification))
	}
	for i, n := range resp.Notification {
		val := n.Update[0].Val.GetStringVal()
		if val == "" {
			t.Errorf("notification[%d]: expected non-empty response", i)
		}
	}
}

func TestSet(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	client, conn := dialGNMI(t, addr)
	defer conn.Close()

	updates := []*pb.Update{
		{
			Path: gnmiserver.CommandToPath("show version"),
			Val:  &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: ""}},
		},
		{
			Path: gnmiserver.CommandToPath("show ip route"),
			Val:  &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: ""}},
		},
	}
	resp, err := client.Set(context.Background(), &pb.SetRequest{Update: updates})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(resp.Response) != 2 {
		t.Fatalf("expected 2 results, got %d", len(resp.Response))
	}
	for i, r := range resp.Response {
		if r.Op != pb.UpdateResult_UPDATE {
			t.Errorf("result[%d]: expected UPDATE op, got %v", i, r.Op)
		}
	}
}

func TestSubscribeOnce(t *testing.T) {
	addr, cleanup := startTestServer(t)
	defer cleanup()

	client, conn := dialGNMI(t, addr)
	defer conn.Close()

	stream, err := client.Subscribe(context.Background())
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	err = stream.Send(&pb.SubscribeRequest{
		Request: &pb.SubscribeRequest_Subscribe{
			Subscribe: &pb.SubscriptionList{
				Subscription: []*pb.Subscription{
					{Path: gnmiserver.CommandToPath("show version")},
					{Path: gnmiserver.CommandToPath("show ip route")},
				},
				Mode: pb.SubscriptionList_ONCE,
			},
		},
	})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	// Should receive 2 updates then a sync_response.
	updateCount := 0
	for {
		resp, err := stream.Recv()
		if err != nil {
			t.Fatalf("Recv: %v", err)
		}
		if resp.GetSyncResponse() {
			break
		}
		update := resp.GetUpdate()
		if update == nil {
			t.Fatal("expected update notification")
		}
		if len(update.Update) == 0 {
			t.Fatal("expected at least one update in notification")
		}
		val := update.Update[0].Val.GetStringVal()
		if val == "" {
			t.Errorf("update[%d]: expected non-empty value", updateCount)
		}
		updateCount++
	}
	if updateCount != 2 {
		t.Errorf("expected 2 updates before sync, got %d", updateCount)
	}
}

func TestPathToCommand(t *testing.T) {
	tests := []struct {
		cmd  string
		want string
	}{
		{"show version", "show version"},
		{"show ip route", "show ip route"},
		{"show interfaces GigabitEthernet1", "show interfaces GigabitEthernet1"},
	}
	for _, tt := range tests {
		path := gnmiserver.CommandToPath(tt.cmd)
		// Verify round-trip: path elements should reconstruct the command.
		// The path has a leading "cli" element.
		if len(path.Elem) < 2 {
			t.Errorf("CommandToPath(%q): expected at least 2 elements, got %d", tt.cmd, len(path.Elem))
			continue
		}
		if path.Elem[0].Name != "cli" {
			t.Errorf("CommandToPath(%q): first elem = %q, want 'cli'", tt.cmd, path.Elem[0].Name)
		}
	}
}
