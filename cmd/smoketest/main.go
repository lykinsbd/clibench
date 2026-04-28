package main

import (
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/httpserver"
	"github.com/lykinsbd/clibench/internal/sshserver"
	"golang.org/x/crypto/ssh"
)

func main() {
	dev, err := device.New("test-rtr", "admin", "admin", "transcripts")
	if err != nil {
		fmt.Fprintf(os.Stderr, "device: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Loaded %d commands\n", len(dev.Commands()))

	// Start SSH on ephemeral port
	sshLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh listen: %v\n", err)
		os.Exit(1)
	}
	sshSrv, err := sshserver.New(sshLn.Addr().String(), dev)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh init: %v\n", err)
		os.Exit(1)
	}
	sshSrv.SetListener(sshLn)
	go sshSrv.ListenAndServe()

	// Start HTTPS on ephemeral port
	httpsLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		fmt.Fprintf(os.Stderr, "https listen: %v\n", err)
		os.Exit(1)
	}
	httpSrv := httpserver.New(httpsLn.Addr().String(), dev)
	httpSrv.SetListener(httpsLn)
	go httpSrv.ListenAndServeTLS()

	time.Sleep(500 * time.Millisecond)

	sshAddr := sshLn.Addr().String()
	httpsAddr := httpsLn.Addr().String()

	// Test HTTPS
	fmt.Println("\n=== HTTPS Test ===")
	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		Timeout:   5 * time.Second,
	}
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://%s/admin/exec/show+version", httpsAddr), nil)
	req.SetBasicAuth("admin", "admin")
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "https error: %v\n", err)
		os.Exit(1)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("HTTPS OK: %d bytes, first line: %s\n", len(body), firstLine(string(body)))

	// Test SSH
	fmt.Println("\n=== SSH Test ===")
	cfg := &ssh.ClientConfig{
		User:            "admin",
		Auth:            []ssh.AuthMethod{ssh.Password("admin")},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	}
	conn, err := ssh.Dial("tcp", sshAddr, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh dial: %v\n", err)
		os.Exit(1)
	}
	sess, err := conn.NewSession()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh session: %v\n", err)
		os.Exit(1)
	}
	out, err := sess.Output("show version")
	sess.Close()
	conn.Close()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ssh exec: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("SSH OK: %d bytes, first line: %s\n", len(out), firstLine(string(out)))

	fmt.Println("\n=== ALL PASS ===")
	os.Exit(0)
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			return s[:i]
		}
	}
	return s
}
