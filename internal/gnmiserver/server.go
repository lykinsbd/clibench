// Package gnmiserver implements a gNMI target that wraps the shared device engine.
// It maps gNMI path elements back to CLI command strings, executes them via
// the device, and returns output as TypedValue string values.
package gnmiserver

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/tlsutil"

	pb "github.com/openconfig/gnmi/proto/gnmi"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
)

// Server is a gNMI target backed by a Device.
type Server struct {
	pb.UnimplementedGNMIServer
	dev      *device.Device
	addr     string
	listener net.Listener
	gs       *grpc.Server
}

// New creates a gNMI server on addr backed by dev.
func New(addr string, dev *device.Device) *Server {
	return &Server{dev: dev, addr: addr}
}

// SetListener sets a custom net.Listener (e.g., one wrapped with latency injection).
func (s *Server) SetListener(ln net.Listener) { s.listener = ln }

// Addr returns the listener's address, or "" if not yet listening.
func (s *Server) Addr() string {
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return ""
}

// Close stops the server gracefully.
func (s *Server) Close() error {
	if s.gs != nil {
		s.gs.GracefulStop()
	}
	return nil
}

// ListenAndServe starts the gRPC/TLS listener. It blocks until Close is called.
func (s *Server) ListenAndServe() error {
	tlsCfg, err := tlsutil.SelfSignedConfig()
	if err != nil {
		return err
	}
	tlsCfg.ClientAuth = tls.NoClientCert

	creds := credentials.NewTLS(tlsCfg)
	s.gs = grpc.NewServer(grpc.Creds(creds))
	pb.RegisterGNMIServer(s.gs, s)

	if s.listener == nil {
		s.listener, err = net.Listen("tcp", s.addr)
		if err != nil {
			return err
		}
	}
	log.Printf("gNMI listening on %s", s.listener.Addr())
	err = s.gs.Serve(s.listener)
	if errors.Is(err, grpc.ErrServerStopped) {
		return nil
	}
	return err
}

// Capabilities returns a minimal capability response.
func (s *Server) Capabilities(_ context.Context, _ *pb.CapabilityRequest) (*pb.CapabilityResponse, error) {
	return &pb.CapabilityResponse{
		SupportedModels:    []*pb.ModelData{{Name: "cli", Organization: "clibench", Version: "1.0.0"}},
		SupportedEncodings: []pb.Encoding{pb.Encoding_ASCII},
		GNMIVersion:        "0.10.0",
	}, nil
}

// Get handles unary Get RPCs — maps gNMI paths to device.Exec().
func (s *Server) Get(_ context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	notifications := make([]*pb.Notification, 0, len(req.GetPath()))
	ts := time.Now().UnixNano()

	for _, path := range req.GetPath() {
		cmd := pathToCommand(path)
		output := s.dev.Exec(cmd)
		notifications = append(notifications, &pb.Notification{
			Timestamp: ts,
			Update: []*pb.Update{{
				Path: path,
				Val:  &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: output}},
			}},
		})
	}
	return &pb.GetResponse{Notification: notifications}, nil
}

// Set handles Set RPCs — maps update paths to device.Exec() calls.
func (s *Server) Set(_ context.Context, req *pb.SetRequest) (*pb.SetResponse, error) {
	results := make([]*pb.UpdateResult, 0, len(req.GetUpdate())+len(req.GetReplace()))

	for _, upd := range req.GetUpdate() {
		cmd := pathToCommand(upd.GetPath())
		s.dev.Exec(cmd)
		results = append(results, &pb.UpdateResult{
			Path: upd.GetPath(),
			Op:   pb.UpdateResult_UPDATE,
		})
	}
	for _, repl := range req.GetReplace() {
		cmd := pathToCommand(repl.GetPath())
		s.dev.Exec(cmd)
		results = append(results, &pb.UpdateResult{
			Path: repl.GetPath(),
			Op:   pb.UpdateResult_REPLACE,
		})
	}
	return &pb.SetResponse{
		Response:  results,
		Timestamp: time.Now().UnixNano(),
	}, nil
}

// Subscribe handles streaming subscriptions (ONCE mode for benchmarking).
func (s *Server) Subscribe(stream grpc.BidiStreamingServer[pb.SubscribeRequest, pb.SubscribeResponse]) error {
	req, err := stream.Recv()
	if err != nil {
		return err
	}

	sub := req.GetSubscribe()
	if sub == nil {
		return fmt.Errorf("first message must be SubscribeRequest with subscribe field")
	}

	ts := time.Now().UnixNano()
	for _, subscription := range sub.GetSubscription() {
		path := subscription.GetPath()
		cmd := pathToCommand(path)
		output := s.dev.Exec(cmd)

		if err := stream.Send(&pb.SubscribeResponse{
			Response: &pb.SubscribeResponse_Update{
				Update: &pb.Notification{
					Timestamp: ts,
					Update: []*pb.Update{{
						Path: path,
						Val:  &pb.TypedValue{Value: &pb.TypedValue_StringVal{StringVal: output}},
					}},
				},
			},
		}); err != nil {
			return err
		}
	}

	// Send sync_response to indicate all data has been sent.
	return stream.Send(&pb.SubscribeResponse{
		Response: &pb.SubscribeResponse_SyncResponse{SyncResponse: true},
	})
}

// pathToCommand converts a gNMI path to a CLI command string.
// e.g., /cli/show/ip/route → "show ip route"
// The leading "cli" element is stripped if present.
func pathToCommand(path *pb.Path) string {
	elems := path.GetElem()
	if len(elems) == 0 {
		return ""
	}
	parts := make([]string, 0, len(elems))
	for i, elem := range elems {
		name := elem.GetName()
		// Skip leading "cli" namespace prefix.
		if i == 0 && name == "cli" {
			continue
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, " ")
}

// CommandToPath converts a CLI command string to a gNMI path.
// e.g., "show ip route" → /cli/show/ip/route
func CommandToPath(cmd string) *pb.Path {
	words := strings.Fields(cmd)
	elems := make([]*pb.PathElem, 0, len(words)+1)
	elems = append(elems, &pb.PathElem{Name: "cli"})
	for _, w := range words {
		elems = append(elems, &pb.PathElem{Name: w})
	}
	return &pb.Path{Elem: elems}
}
