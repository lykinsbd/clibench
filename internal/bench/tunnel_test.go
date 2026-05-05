package bench

import (
	"net"
	"testing"

	"github.com/lykinsbd/clibench/internal/headend"
	"github.com/lykinsbd/clibench/internal/proxy"
)

func setupTunnel(t *testing.T) (httpsHeadendAddr, h3HeadendAddr string) {
	t.Helper()
	sshAddr, _ := setupServers(t) // starts SSH + HTTPS device servers

	// Site proxy (HTTPS → SSH backend)
	siteLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := proxy.New(siteLn.Addr().String(), sshAddr, "admin", "admin", true)
	p.SetListener(siteLn)
	go p.ListenAndServeTLS()
	t.Cleanup(func() { p.Close() })

	// Headend proxy (HTTPS WAN)
	hHTTPSLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hHTTPS, err := headend.New(hHTTPSLn.Addr().String(), "https://"+siteLn.Addr().String(), "admin", "admin", "https")
	if err != nil {
		t.Fatal(err)
	}
	hHTTPS.SetListener(hHTTPSLn)
	go hHTTPS.ListenAndServe()
	t.Cleanup(func() { hHTTPS.Close() })

	// Headend proxy (HTTP/3 WAN) — still talks HTTPS to site proxy for now
	hH3Ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	hH3, err := headend.New(hH3Ln.Addr().String(), "https://"+siteLn.Addr().String(), "admin", "admin", "https")
	if err != nil {
		t.Fatal(err)
	}
	hH3.SetListener(hH3Ln)
	go hH3.ListenAndServe()
	t.Cleanup(func() { hH3.Close() })

	waitTCP(t, siteLn.Addr().String())
	waitTCP(t, hHTTPSLn.Addr().String())
	waitTCP(t, hH3Ln.Addr().String())
	return hHTTPSLn.Addr().String(), hH3Ln.Addr().String()
}

func TestTunnel(t *testing.T) {
	httpsAddr, h3Addr := setupTunnel(t)
	cfg := baseCfg("")
	results := Tunnel(TunnelConfig{
		Config:           cfg,
		HTTPSHeadendAddr: httpsAddr,
		H3HeadendAddr:    h3Addr,
	})
	// ssh-https-ssh, ssh-https-ssh-batch, ssh-http3-ssh, ssh-http3-ssh-batch = 4 modes
	assertResults(t, results, "tunnel", 4)
}
