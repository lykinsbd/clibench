package main

import (
	"flag"
	"fmt"
	"log"

	"github.com/lykinsbd/clibench/internal/device"
	"github.com/lykinsbd/clibench/internal/httpserver"
	"github.com/lykinsbd/clibench/internal/sshserver"
)

func main() {
	sshPort := flag.Int("ssh-port", 2222, "SSH listen port")
	httpsPort := flag.Int("https-port", 8443, "HTTPS listen port")
	hostname := flag.String("hostname", "benchmark-rtr", "Device hostname")
	user := flag.String("user", "admin", "Username")
	pass := flag.String("pass", "admin", "Password")
	transcripts := flag.String("transcripts", "transcripts", "Transcript directory")
	flag.Parse()

	dev, err := device.New(*hostname, *user, *pass, *transcripts)
	if err != nil {
		log.Fatalf("device init: %v", err)
	}
	log.Printf("Device %q loaded %d commands", *hostname, len(dev.Commands()))

	sshSrv, err := sshserver.New(fmt.Sprintf(":%d", *sshPort), dev)
	if err != nil {
		log.Fatalf("ssh server init: %v", err)
	}
	go func() {
		if err := sshSrv.ListenAndServe(); err != nil {
			log.Fatalf("ssh: %v", err)
		}
	}()

	httpSrv := httpserver.New(fmt.Sprintf(":%d", *httpsPort), dev)
	if err := httpSrv.ListenAndServeTLS(); err != nil {
		log.Fatalf("https: %v", err)
	}
}
