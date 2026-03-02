package vless

import (
	"io"
	"net"
	"testing"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

type rwc struct {
	io.ReadWriter
}

func (r *rwc) Close() error { return nil }

func TestVLESS_NameAndFeatures(t *testing.T) {
	p := &VLESSProtocol{}
	if p.Name() != ProtocolName {
		t.Fatalf("unexpected name: %s", p.Name())
	}
	f := p.Features()
	if len(f.Transports) == 0 || f.Transports[0] != "tcp" {
		t.Fatalf("unexpected transports: %#v", f.Transports)
	}
	if !f.Experimental {
		t.Fatalf("expected Experimental=true for vless adapter")
	}
}

func TestVLESS_Handshake_Success(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	token := "secret-token"
	p := &VLESSProtocol{}

	clientCfg := &protoiface.Config{
		Token:   token,
		Timeout: 2 * time.Second,
		Raw:     map[string]any{cfgKeyRole: roleClient},
	}
	serverCfg := &protoiface.Config{
		Token:   token,
		Timeout: 2 * time.Second,
		Raw:     map[string]any{cfgKeyRole: roleServer},
	}

	errCh := make(chan error, 2)

	go func() {
		_, err := p.Handshake(clientConn, clientCfg)
		errCh <- err
	}()

	go func() {
		_, err := p.Handshake(serverConn, serverCfg)
		errCh <- err
	}()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("handshake error: %v", err)
		}
	}
}

func TestVLESS_Handshake_InvalidToken(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer clientConn.Close()
	defer serverConn.Close()

	p := &VLESSProtocol{}

	clientCfg := &protoiface.Config{
		Token:   "good-token",
		Timeout: 2 * time.Second,
		Raw:     map[string]any{cfgKeyRole: roleClient},
	}
	serverCfg := &protoiface.Config{
		Token:   "bad-token",
		Timeout: 2 * time.Second,
		Raw:     map[string]any{cfgKeyRole: roleServer},
	}

	errCh := make(chan error, 2)

	go func() {
		_, err := p.Handshake(clientConn, clientCfg)
		errCh <- err
	}()

	go func() {
		_, err := p.Handshake(serverConn, serverCfg)
		errCh <- err
	}()

	// Ожидаем хотя бы одну ошибку (на стороне сервера).
	var gotErr bool
	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			gotErr = true
		}
	}
	if !gotErr {
		t.Fatalf("expected handshake error with invalid token, got none")
	}
}

