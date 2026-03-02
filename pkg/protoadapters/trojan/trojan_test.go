package trojan

import (
	"net"
	"testing"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

func TestTrojanNameAndFeatures(t *testing.T) {
	p := &TrojanProtocol{}
	if p.Name() != ProtocolName {
		t.Fatalf("expected name %q, got %q", ProtocolName, p.Name())
	}
	feat := p.Features()
	if len(feat.Transports) == 0 {
		t.Fatalf("expected non-empty transports")
	}
}

func TestTrojanHandshake_Success(t *testing.T) {
	const token = "secret-token"

	// Используем in-memory двунаправленное соединение.
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	p := &TrojanProtocol{}

	serverCfg := &protoiface.Config{
		Token:   token,
		Timeout: 2 * time.Second,
		Raw:     map[string]any{"role": "server"},
	}
	clientCfg := &protoiface.Config{
		Token:   token,
		Timeout: 2 * time.Second,
		Raw:     map[string]any{"role": "client"},
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := p.Handshake(serverSide, serverCfg)
		errCh <- err
	}()

	if _, err := p.Handshake(clientSide, clientCfg); err != nil {
		t.Fatalf("client handshake failed: %v", err)
	}

	if err := <-errCh; err != nil {
		t.Fatalf("server handshake failed: %v", err)
	}
}

func TestTrojanHandshake_InvalidToken(t *testing.T) {
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	p := &TrojanProtocol{}

	serverCfg := &protoiface.Config{
		Token:   "server-token",
		Timeout: 2 * time.Second,
		Raw:     map[string]any{"role": "server"},
	}
	clientCfg := &protoiface.Config{
		Token:   "client-token",
		Timeout: 2 * time.Second,
		Raw:     map[string]any{"role": "client"},
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := p.Handshake(serverSide, serverCfg)
		errCh <- err
	}()

	if _, err := p.Handshake(clientSide, clientCfg); err == nil {
		t.Fatalf("expected client handshake to fail with invalid token")
	}

	if err := <-errCh; err == nil {
		t.Fatalf("expected server handshake to fail with invalid token")
	}
}

