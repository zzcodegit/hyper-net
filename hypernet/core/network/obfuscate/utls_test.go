package obfuscate

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"

	utls "github.com/refraction-networking/utls"
)

func TestRandomClientHelloID_ReturnsValid(t *testing.T) {
	// RandomClientHelloID cycles through browser IDs; ensure it doesn't panic and returns
	for i := 0; i < 6; i++ {
		id := RandomClientHelloID()
		if id == (utls.ClientHelloID{}) {
			t.Error("ClientHelloID should not be zero value")
		}
	}
}

func TestTlsConfigToUtls_NilConfig_UsesServerName(t *testing.T) {
	uc := tlsConfigToUtls(nil, "example.com")
	if uc == nil {
		t.Fatal("tlsConfigToUtls(nil, ...) should not return nil")
	}
	if uc.ServerName != "example.com" {
		t.Errorf("ServerName = %q, want example.com", uc.ServerName)
	}
}

func TestTlsConfigToUtls_NonNilConfig_CopiesFields(t *testing.T) {
	rootCAs, _ := x509.SystemCertPool()
	if rootCAs == nil {
		rootCAs = x509.NewCertPool()
	}
	base := &tls.Config{
		RootCAs:            rootCAs,
		InsecureSkipVerify: true,
		NextProtos:         []string{"h2", "http/1.1"},
		ServerName:         "original.com",
	}
	uc := tlsConfigToUtls(base, "override.com")
	if uc.ServerName != "override.com" {
		t.Errorf("ServerName = %q, want override.com", uc.ServerName)
	}
	if !uc.InsecureSkipVerify {
		t.Error("InsecureSkipVerify should be true")
	}
	if len(uc.NextProtos) != 2 {
		t.Errorf("NextProtos len = %d, want 2", len(uc.NextProtos))
	}
}

func TestListenUTLS_ReturnsListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer ln.Close()
	tlsCfg := &tls.Config{InsecureSkipVerify: true}
	wrapped := ListenUTLS(ln, tlsCfg)
	if wrapped == nil {
		t.Fatal("ListenUTLS returned nil")
	}
	_ = wrapped.Close()
}

// TestDialUTLS_LocalTLS verifies DialUTLS completes a handshake with a local TLS server.
func TestDialUTLS_LocalTLS(t *testing.T) {
	cert, key := generateSelfSignedCert(t)
	tlsCfg := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{cert}, PrivateKey: key}},
	}
	ln, err := tls.Listen("tcp", "127.0.0.1:0", tlsCfg)
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Keep connection open until client closes so handshake can complete
		buf := make([]byte, 1)
		_, _ = conn.Read(buf)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	addr := ln.Addr().String()
	clientCfg := &tls.Config{InsecureSkipVerify: true}
	conn, err := DialUTLS(ctx, "tcp", addr, "localhost", clientCfg)
	if err != nil {
		t.Fatalf("DialUTLS: %v", err)
	}
	defer conn.Close()

	// Connection established; uTLS handshake succeeded
	uconn, ok := conn.(*utls.UConn)
	if !ok {
		t.Fatal("expected *utls.UConn")
	}
	state := uconn.ConnectionState()
	if !state.HandshakeComplete {
		t.Error("handshake should be complete")
	}
}

func generateSelfSignedCert(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(time.Hour),
		BasicConstraintsValid: true,
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create cert: %v", err)
	}
	return certDER, key
}

