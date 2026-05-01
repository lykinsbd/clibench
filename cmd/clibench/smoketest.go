package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/quic-go/quic-go/http3"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/http3server"
	"github.com/lykinsbd/clibench/internal/httpserver"
	"github.com/lykinsbd/clibench/internal/sshserver"
	"golang.org/x/crypto/ssh"
)

// SmoketestCmd runs a quick integration smoke test.
type SmoketestCmd struct {
	Transcripts string `help:"Transcript directory." default:"transcripts"`
}

// Run executes the smoke test.
func (s *SmoketestCmd) Run() error {
	dev, err := device.New("test-rtr", "admin", "admin", s.Transcripts)
	if err != nil {
		return fmt.Errorf("device: %w", err)
	}
	fmt.Printf("Loaded %d commands\n", len(dev.Commands()))

	// Start SSH
	sshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	sshSrv, err := sshserver.New(sshLn.Addr().String(), dev)
	if err != nil {
		return err
	}
	sshSrv.SetListener(sshLn)
	go sshSrv.ListenAndServe()

	// Start HTTPS
	httpsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	httpSrv := httpserver.New(httpsLn.Addr().String(), dev)
	httpSrv.SetListener(httpsLn)
	go httpSrv.ListenAndServeTLS()

	// Start HTTP/3
	h3Conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	h3Srv := http3server.New(h3Conn.LocalAddr().String(), dev)
	h3Srv.SetPacketConn(h3Conn)
	go h3Srv.ListenAndServe()

	time.Sleep(500 * time.Millisecond)

	// Test HTTPS
	fmt.Println("\n=== HTTPS Test ===")
	httpsBody, err := doHTTPS(httpsLn.Addr().String())
	if err != nil {
		return fmt.Errorf("https: %w", err)
	}
	fmt.Printf("HTTPS OK: %d bytes, first line: %s\n", len(httpsBody), firstLine(httpsBody))

	// Test SSH
	fmt.Println("\n=== SSH Test ===")
	sshBody, err := doSSH(sshLn.Addr().String())
	if err != nil {
		return fmt.Errorf("ssh: %w", err)
	}
	fmt.Printf("SSH OK: %d bytes, first line: %s\n", len(sshBody), firstLine(sshBody))

	// Test HTTP/3
	fmt.Println("\n=== HTTP/3 Test ===")
	h3Body, err := doHTTP3(h3Conn.LocalAddr().String())
	if err != nil {
		return fmt.Errorf("http3: %w", err)
	}
	fmt.Printf("HTTP/3 OK: %d bytes, first line: %s\n", len(h3Body), firstLine(h3Body))

	fmt.Println("\n=== ALL PASS ===")
	return nil
}

func doHTTPS(addr string) (string, error) {
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://%s/admin/exec/show+version", addr), nil)
	req.SetBasicAuth("admin", "admin")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(body), nil
}

func doSSH(addr string) (string, error) {
	cfg := &ssh.ClientConfig{
		User:            "admin",
		Auth:            []ssh.AuthMethod{ssh.Password("admin")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, err := ssh.Dial("tcp", addr, cfg)
	if err != nil {
		return "", err
	}
	sess, err := conn.NewSession()
	if err != nil {
		conn.Close()
		return "", err
	}
	out, err := sess.Output("show version")
	sess.Close()
	conn.Close()
	return string(out), err
}

func doHTTP3(addr string) (string, error) {
	client := &http.Client{
		Transport: &http3.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: true,
				NextProtos:         []string{http3.NextProtoH3},
			},
		},
		Timeout: 5 * time.Second,
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://%s/admin/exec/show+version", addr), nil)
	req.SetBasicAuth("admin", "admin")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return string(body), nil
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
