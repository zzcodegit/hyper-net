package shadowsocks

import (
	"bytes"
	"io"
	"net"
	"testing"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

type memStream struct {
	io.ReadWriter
	closed bool
}

func (m *memStream) Close() error {
	m.closed = true
	return nil
}

func TestShadowsocksNameAndFeatures(t *testing.T) {
	p := &ShadowsocksProtocol{}
	if p.Name() != ProtocolName {
		t.Fatalf("expected name %q, got %q", ProtocolName, p.Name())
	}
	feat := p.Features()
	if feat.Encryption != "AES-256-GCM (AEAD)" {
		t.Fatalf("expected AES-256-GCM, got %q", feat.Encryption)
	}
}

func TestShadowsocksHandshakeEncrypted(t *testing.T) {
	const password = "test-password"
	cConn, sConn := net.Pipe()
	defer cConn.Close()
	defer sConn.Close()

	p := &ShadowsocksProtocol{}
	cfgClient := &protoiface.Config{
		Token: password,
		Raw:   map[string]any{"role": "client"},
	}
	cfgServer := &protoiface.Config{
		Token: password,
		Raw:   map[string]any{"role": "server"},
	}

	var clientSess io.ReadWriteCloser
	var clientErr error
	done := make(chan struct{})
	go func() {
		clientSess, clientErr = p.Handshake(cConn, cfgClient)
		close(done)
	}()

	serverSess, err := p.Handshake(sConn, cfgServer)
	if err != nil {
		t.Fatalf("server handshake: %v", err)
	}
	<-done
	if clientErr != nil {
		t.Fatalf("client handshake: %v", clientErr)
	}
	defer clientSess.Close()
	defer serverSess.Close()

	msg := []byte("hello")
	buf := make([]byte, 32)
	readDone := make(chan struct{})
	var readN int
	var readErr error
	go func() {
		readN, readErr = serverSess.Read(buf)
		close(readDone)
	}()
	if _, err := clientSess.Write(msg); err != nil {
		t.Fatalf("client write: %v", err)
	}
	<-readDone
	if readErr != nil && readErr != io.EOF {
		t.Fatalf("server read: %v", readErr)
	}
	if !bytes.Equal(buf[:readN], msg) {
		t.Fatalf("expected %q, got %q", msg, buf[:readN])
	}
}

func TestShadowsocksHandshakeMissingRole(t *testing.T) {
	p := &ShadowsocksProtocol{}
	buf := &bytes.Buffer{}
	s := &memStream{ReadWriter: buf}
	cfg := &protoiface.Config{Token: "pass", Raw: map[string]any{}}
	_, err := p.Handshake(s, cfg)
	if err == nil {
		t.Fatal("expected error when role missing")
	}
}

func TestDeriveKey(t *testing.T) {
	k1 := deriveKey("foo")
	k2 := deriveKey("foo")
	if !bytes.Equal(k1, k2) {
		t.Fatal("same password must yield same key")
	}
	if len(k1) != 32 {
		t.Fatalf("key must be 32 bytes, got %d", len(k1))
	}
}
