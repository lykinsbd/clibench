package main

import (
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/http3server"
	"github.com/lykinsbd/clibench/internal/httpserver"
	"github.com/lykinsbd/clibench/internal/sshserver"
)

// ServerCmd starts a standalone multi-protocol server.
type ServerCmd struct {
	SSHPort     int    `help:"SSH listen port." default:"2222"`
	HTTPSPort   int    `help:"HTTPS listen port." default:"8443"`
	HTTP3Port   int    `help:"HTTP/3 listen port." default:"8444"`
	Hostname    string `help:"Device hostname." default:"bench-rtr"`
	User        string `help:"Username." default:"admin" short:"u"`
	Pass        string `help:"Password." default:"admin" short:"p"`
	Transcripts string `help:"Transcript directory." default:"transcripts"`
}

// Run starts the server and blocks until interrupted.
func (s *ServerCmd) Run() error {
	dev, err := device.New(s.Hostname, s.User, s.Pass, s.Transcripts)
	if err != nil {
		return fmt.Errorf("device init: %w", err)
	}
	log.Printf("Device %q loaded %d commands", s.Hostname, len(dev.Commands()))

	sshSrv, err := sshserver.New(fmt.Sprintf(":%d", s.SSHPort), dev)
	if err != nil {
		return fmt.Errorf("ssh server init: %w", err)
	}
	go sshSrv.ListenAndServe()

	httpSrv := httpserver.New(fmt.Sprintf(":%d", s.HTTPSPort), dev)
	go httpSrv.ListenAndServeTLS()

	h3Srv := http3server.New(fmt.Sprintf(":%d", s.HTTP3Port), dev)
	go h3Srv.ListenAndServe()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Printf("Received %v, shutting down", sig)
	sshSrv.Close()
	httpSrv.Close()
	h3Srv.Close()
	return nil
}
