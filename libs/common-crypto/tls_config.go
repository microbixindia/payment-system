package commoncrypto

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

var (
	// ErrSubstandardTLSVersion is returned when a client attempts a handshake with TLS < 1.3.
	ErrSubstandardTLSVersion = errors.New("commoncrypto/tls: connection rejected - strictly requires TLS 1.3")

	// ErrSubstandardCipherSuite is returned when the negotiated cipher suite does not meet enterprise criteria.
	ErrSubstandardCipherSuite = errors.New("commoncrypto/tls: connection rejected - cipher suite is below enterprise grade")

	// ErrClientCertificateRequired is returned when mTLS fails to supply client certificate.
	ErrClientCertificateRequired = errors.New("commoncrypto/tls: mutual TLS authentication failed - no valid client certificate presented")
)

// EnterpriseAllowedCipherSuites explicitly white-lists only high-grade AES-256 and Poly1305 AEAD suites.
var EnterpriseAllowedCipherSuites = map[uint16]string{
	tls.TLS_AES_256_GCM_SHA384:       "TLS_AES_256_GCM_SHA384",
	tls.TLS_CHACHA20_POLY1305_SHA256: "TLS_CHACHA20_POLY1305_SHA256",
}

// EnterpriseTLSConfig holds parameters to configure hardened TLS 1.3 endpoints.
type EnterpriseTLSConfig struct {
	CertFilePath       string
	KeyFilePath        string
	ClientCAPath       string
	ServerName         string
	RequireClientAuth  bool
	InsecureSkipVerify bool // Strongly discouraged, only for test mocks
}

// BuildHardenedTLSConfig assembles a strict crypto/tls.Config enforcing TLS 1.3,
// Curve preferences (X25519, CurveP384), and connection verification hooks that drop substandard handshakes.
func BuildHardenedTLSConfig(cfg EnterpriseTLSConfig) (*tls.Config, error) {
	tlsConf := &tls.Config{
		MinVersion: tls.VersionTLS13,
		MaxVersion: tls.VersionTLS13,
		CurvePreferences: []tls.CurveID{
			tls.X25519,
			tls.CurveP384,
		},
		PreferServerCipherSuites: true,
		ServerName:               cfg.ServerName,
	}

	// Load server key pair if provided
	if cfg.CertFilePath != "" && cfg.KeyFilePath != "" {
		cert, err := tls.LoadX509KeyPair(cfg.CertFilePath, cfg.KeyFilePath)
		if err != nil {
			return nil, fmt.Errorf("commoncrypto/tls: failed to load server certificate pair: %w", err)
		}
		tlsConf.Certificates = []tls.Certificate{cert}
	}

	// Configure mTLS client authentication if requested
	if cfg.RequireClientAuth {
		if cfg.ClientCAPath == "" {
			return nil, errors.New("commoncrypto/tls: client CA path must be specified when RequireClientAuth is enabled")
		}

		caData, err := os.ReadFile(cfg.ClientCAPath)
		if err != nil {
			return nil, fmt.Errorf("commoncrypto/tls: failed reading client CA file: %w", err)
		}

		caPool := x509.NewCertPool()
		if !caPool.AppendCertsFromPEM(caData) {
			return nil, errors.New("commoncrypto/tls: failed to parse client CA certificates from PEM")
		}

		tlsConf.ClientCAs = caPool
		tlsConf.ClientAuth = tls.RequireAndVerifyClientCert
	}

	// VerifyConnection hook guarantees post-handshake inspection:
	// Drops connection if version < TLS 1.3 or cipher suite is substandard.
	tlsConf.VerifyConnection = func(cs tls.ConnectionState) error {
		if cs.Version < tls.VersionTLS13 {
			return fmt.Errorf("%w: attempted TLS version 0x%04x", ErrSubstandardTLSVersion, cs.Version)
		}

		// Verify cipher suite against enterprise whitelist
		if _, ok := EnterpriseAllowedCipherSuites[cs.CipherSuite]; !ok {
			return fmt.Errorf("%w: negotiated cipher suite 0x%04x not permitted", ErrSubstandardCipherSuite, cs.CipherSuite)
		}

		if cfg.RequireClientAuth && len(cs.PeerCertificates) == 0 {
			return ErrClientCertificateRequired
		}

		return nil
	}

	return tlsConf, nil
}
