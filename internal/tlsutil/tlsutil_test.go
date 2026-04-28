package tlsutil

import (
	"crypto/x509"
	"testing"
	"time"
)

func TestSelfSignedConfigValid(t *testing.T) {
	cfg, err := SelfSignedConfig()
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Certificates) != 1 {
		t.Fatalf("got %d certs, want 1", len(cfg.Certificates))
	}
	cert, err := x509.ParseCertificate(cfg.Certificates[0].Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || now.After(cert.NotAfter) {
		t.Errorf("cert not valid at current time: notBefore=%v notAfter=%v", cert.NotBefore, cert.NotAfter)
	}
}
