package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

// LoadOrGenerateSelfSigned loads an existing TLS certificate from dataDir, or
// generates a new self-signed one if none exists or the current cert expires
// within 7 days. Additional hostnames/IPs can be provided via hosts to be
// included in the certificate SANs. Called once at server startup.
func LoadOrGenerateSelfSigned(dataDir string, hosts ...string) (tls.Certificate, error) {
	certPath := filepath.Join(dataDir, "cert.pem")
	keyPath := filepath.Join(dataDir, "key.pem")

	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		// Check if cert is expired or expiring within 7 days
		if leaf, parseErr := x509.ParseCertificate(cert.Certificate[0]); parseErr == nil {
			if time.Now().Add(7 * 24 * time.Hour).Before(leaf.NotAfter) {
				return cert, nil
			}
			// Cert expired or expiring soon, regenerate
		}
	}

	return generateAndSave(certPath, keyPath, hosts)
}

// generateAndSave creates a new ECDSA P-256 self-signed certificate valid for
// one year, writes the PEM-encoded cert and key to disk, and returns the parsed
// TLS certificate. The certificate includes localhost, 127.0.0.1, all local
// network IPs, and any additional hosts provided.
func generateAndSave(certPath, keyPath string, hosts []string) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate serial: %w", err)
	}

	ips, dnsNames := collectSANs(hosts)

	// Log the SANs so operators know what IP addresses are in the cert.
	// The cert is visible to anyone connecting to the server, so this is not
	// a secret — but operators should know which IPs were auto-detected from
	// their network interfaces.
	log.Printf("tls: generating self-signed cert with DNS SANs %v and IP SANs %v", dnsNames, ips)

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"Appx Self-Signed"}},
		NotBefore:    time.Now(),
		NotAfter:     time.Now().Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  ips,
		DNSNames:     dnsNames,
	}

	certDER, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certDER})

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("marshal key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(filepath.Dir(certPath), 0700); err != nil {
		return tls.Certificate{}, fmt.Errorf("create dir: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0600); err != nil {
		return tls.Certificate{}, fmt.Errorf("write cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return tls.Certificate{}, fmt.Errorf("write key: %w", err)
	}

	// Print a one-time trust instruction. Without this, browsers show a
	// certificate warning for every new subdomain (*.localhost project).
	// Trusting the cert as a root CA silences all warnings permanently.
	log.Printf("tls: to trust this cert in Chrome/Safari on macOS, run:")
	log.Printf("tls:   sudo security add-trusted-cert -d -r trustRoot -k /Library/Keychains/System.keychain %s", certPath)
	log.Printf("tls: then restart your browser. Only needed once per machine.")

	return tls.X509KeyPair(certPEM, keyPEM)
}

// collectSANs builds the Subject Alternative Names for the self-signed
// certificate. It always includes localhost and 127.0.0.1, adds any explicitly
// provided hosts, and auto-detects non-loopback IPs from the machine's network
// interfaces so the cert is valid when accessed via LAN IP.
func collectSANs(hosts []string) ([]net.IP, []string) {
	ipSet := map[string]net.IP{
		"127.0.0.1": net.ParseIP("127.0.0.1"),
	}
	dnsSet := map[string]bool{
		"localhost":   true,
		"*.localhost": true,
	}

	// Add explicitly provided hosts
	for _, h := range hosts {
		if ip := net.ParseIP(h); ip != nil {
			ipSet[h] = ip
		} else {
			dnsSet[h] = true
		}
	}

	// Detect local network IPs
	ifaces, err := net.Interfaces()
	if err == nil {
		for _, iface := range ifaces {
			if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
				continue
			}
			addrs, err := iface.Addrs()
			if err != nil {
				continue
			}
			for _, addr := range addrs {
				var ip net.IP
				switch v := addr.(type) {
				case *net.IPNet:
					ip = v.IP
				case *net.IPAddr:
					ip = v.IP
				}
				if ip != nil && !ip.IsLoopback() {
					ipSet[ip.String()] = ip
				}
			}
		}
	}

	ips := make([]net.IP, 0, len(ipSet))
	for _, ip := range ipSet {
		ips = append(ips, ip)
	}

	dnsNames := make([]string, 0, len(dnsSet))
	for name := range dnsSet {
		dnsNames = append(dnsNames, name)
	}

	return ips, dnsNames
}
