package commoncrypto

import (
	"crypto/tls"
	"testing"
)

func TestBuildHardenedTLSConfig(t *testing.T) {
	cfg := EnterpriseTLSConfig{
		ServerName: "gateway.datavendor.internal",
	}

	tlsConf, err := BuildHardenedTLSConfig(cfg)
	if err != nil {
		t.Fatalf("failed to build hardened TLS config: %v", err)
	}

	if tlsConf.MinVersion != tls.VersionTLS13 {
		t.Errorf("expected MinVersion TLS 1.3 (0x%04x), got 0x%04x", tls.VersionTLS13, tlsConf.MinVersion)
	}
	if tlsConf.MaxVersion != tls.VersionTLS13 {
		t.Errorf("expected MaxVersion TLS 1.3 (0x%04x), got 0x%04x", tls.VersionTLS13, tlsConf.MaxVersion)
	}

	// Verify curve preferences contain X25519 and CurveP384
	if len(tlsConf.CurvePreferences) != 2 {
		t.Fatalf("expected 2 curve preferences, got %d", len(tlsConf.CurvePreferences))
	}
	if tlsConf.CurvePreferences[0] != tls.X25519 {
		t.Errorf("first curve preference must be X25519, got %v", tlsConf.CurvePreferences[0])
	}
	if tlsConf.CurvePreferences[1] != tls.CurveP384 {
		t.Errorf("second curve preference must be CurveP384, got %v", tlsConf.CurvePreferences[1])
	}

	// Test connection verification hook with TLS 1.2 attempt (must fail)
	badState := tls.ConnectionState{
		Version:     tls.VersionTLS12,
		CipherSuite: tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	}
	if err := tlsConf.VerifyConnection(badState); err == nil {
		t.Errorf("expected VerifyConnection to reject TLS 1.2 handshake, but it passed")
	}

	// Test connection verification hook with substandard TLS 1.3 cipher suite (must fail)
	badCipherState := tls.ConnectionState{
		Version:     tls.VersionTLS13,
		CipherSuite: tls.TLS_AES_128_GCM_SHA256, // not in EnterpriseAllowedCipherSuites
	}
	if err := tlsConf.VerifyConnection(badCipherState); err == nil {
		t.Errorf("expected VerifyConnection to reject AES-128 cipher suite, but it passed")
	}

	// Test connection verification hook with enterprise TLS 1.3 cipher suite (must pass)
	goodState := tls.ConnectionState{
		Version:     tls.VersionTLS13,
		CipherSuite: tls.TLS_AES_256_GCM_SHA384,
	}
	if err := tlsConf.VerifyConnection(goodState); err != nil {
		t.Errorf("expected VerifyConnection to permit TLS_AES_256_GCM_SHA384, but got: %v", err)
	}
}
