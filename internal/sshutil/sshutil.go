// Package sshutil provides shared SSH server scaffolding used by
// sshserver and headend.
package sshutil

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net"

	"golang.org/x/crypto/ssh"
)

// SessionHandler is called for each accepted session channel.
type SessionHandler func(ch ssh.Channel, reqs <-chan *ssh.Request)

// ServerConfig creates an ssh.ServerConfig with ed25519 host key and
// password authentication.
func ServerConfig(user, pass string) (*ssh.ServerConfig, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return nil, err
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, p []byte) (*ssh.Permissions, error) {
			if c.User() == user && string(p) == pass {
				return nil, nil
			}
			return nil, fmt.Errorf("auth failed")
		},
	}
	cfg.AddHostKey(signer)
	return cfg, nil
}

// Serve accepts connections on ln and dispatches sessions to handler.
// Returns nil when ln is closed.
func Serve(ln net.Listener, cfg *ssh.ServerConfig, handler SessionHandler) error {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go handleConn(conn, cfg, handler)
	}
}

func handleConn(c net.Conn, cfg *ssh.ServerConfig, handler SessionHandler) {
	defer c.Close()
	sConn, chans, reqs, err := ssh.NewServerConn(c, cfg)
	if err != nil {
		return
	}
	defer sConn.Close()
	go ssh.DiscardRequests(reqs)

	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "unsupported")
			continue
		}
		ch, requests, err := newCh.Accept()
		if err != nil {
			continue
		}
		go handler(ch, requests)
	}
}

// Listen creates a listener and logs the address. If ln is non-nil, uses it.
func Listen(ln net.Listener, addr, label string) (net.Listener, error) {
	if ln != nil {
		log.Printf("%s listening on %s", label, ln.Addr())
		return ln, nil
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	log.Printf("%s listening on %s", label, l.Addr())
	return l, nil
}
