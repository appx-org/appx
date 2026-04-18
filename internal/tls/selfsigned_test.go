package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCollectSANs_Defaults(t *testing.T) {
	ips, dnsNames := collectSANs(nil)

	// Must always include 127.0.0.1
	found := false
	for _, ip := range ips {
		if ip.Equal(net.ParseIP("127.0.0.1")) {
			found = true
		}
	}
	if !found {
		t.Error("expected 127.0.0.1 in IPs")
	}

	// Must always include localhost
	found = false
	for _, name := range dnsNames {
		if name == "localhost" {
			found = true
		}
	}
	if !found {
		t.Error("expected localhost in DNS names")
	}
}

func TestCollectSANs_IPHost(t *testing.T) {
	ips, _ := collectSANs([]string{"1.2.3.4"})

	found := false
	for _, ip := range ips {
		if ip.Equal(net.ParseIP("1.2.3.4")) {
			found = true
		}
	}
	if !found {
		t.Error("expected 1.2.3.4 in IPs")
	}
}

func TestCollectSANs_DNSHost(t *testing.T) {
	_, dnsNames := collectSANs([]string{"my.server.com"})

	found := false
	for _, name := range dnsNames {
		if name == "my.server.com" {
			found = true
		}
	}
	if !found {
		t.Error("expected my.server.com in DNS names")
	}
}

func TestCollectSANs_MixedHosts(t *testing.T) {
	ips, dnsNames := collectSANs([]string{"10.0.0.1", "example.com"})

	foundIP := false
	for _, ip := range ips {
		if ip.Equal(net.ParseIP("10.0.0.1")) {
			foundIP = true
		}
	}
	if !foundIP {
		t.Error("expected 10.0.0.1 in IPs")
	}

	foundDNS := false
	for _, name := range dnsNames {
		if name == "example.com" {
			foundDNS = true
		}
	}
	if !foundDNS {
		t.Error("expected example.com in DNS names")
	}
}

func TestCollectSANs_IncludesWildcardLocalhost(t *testing.T) {
	_, dnsNames := collectSANs(nil)

	found := false
	for _, name := range dnsNames {
		if name == "*.localhost" {
			found = true
		}
	}
	if !found {
		t.Error("expected *.localhost in DNS names")
	}
}

func TestLoadOrGenerate_CreatesFreshCert(t *testing.T) {
	dir := t.TempDir()

	cert, err := LoadOrGenerateSelfSigned(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cert.Certificate) == 0 {
		t.Error("expected non-empty certificate")
	}

	// Files should exist on disk
	if _, err := os.Stat(filepath.Join(dir, "cert.pem")); err != nil {
		t.Error("cert.pem not created")
	}
	if _, err := os.Stat(filepath.Join(dir, "key.pem")); err != nil {
		t.Error("key.pem not created")
	}
}

func TestLoadOrGenerate_ReusesValidCert(t *testing.T) {
	dir := t.TempDir()

	cert1, err := LoadOrGenerateSelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}

	cert2, err := LoadOrGenerateSelfSigned(dir)
	if err != nil {
		t.Fatal(err)
	}

	// Both calls should return the same certificate
	leaf1, _ := x509.ParseCertificate(cert1.Certificate[0])
	leaf2, _ := x509.ParseCertificate(cert2.Certificate[0])
	if leaf1.SerialNumber.Cmp(leaf2.SerialNumber) != 0 {
		t.Error("expected same cert to be reused")
	}
}

func TestLoadOrGenerate_RegeneratesExpiringSoon(t *testing.T) {
	dir := t.TempDir()

	// Write a cert that expires in 3 days (within the 7-day renewal window)
	if err := writeCertExpiringIn(dir, 3*24*time.Hour); err != nil {
		t.Fatal(err)
	}

	cert, err := LoadOrGenerateSelfSigned(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	leaf, _ := x509.ParseCertificate(cert.Certificate[0])
	if time.Until(leaf.NotAfter) < 7*24*time.Hour {
		t.Error("expected regenerated cert to have long validity")
	}
}

// writeCertExpiringIn writes a self-signed cert to dir that expires after the
// given duration, used to simulate an expiring certificate in tests.
func writeCertExpiringIn(dir string, validity time.Duration) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"Test"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return err
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), certPEM, 0600); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "key.pem"), keyPEM, 0600)
}
