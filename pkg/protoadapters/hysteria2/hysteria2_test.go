package hysteria2

import (
	"bytes"
	"io"
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

func TestHysteria2NameAndFeatures(t *testing.T) {
	p := &Hysteria2Protocol{}
	if p.Name() != ProtocolName {
		t.Fatalf("expected name %q, got %q", ProtocolName, p.Name())
	}
	feat := p.Features()
	if !feat.Mux {
		t.Fatalf("expected mux=true for hysteria2")
	}
	if feat.Encryption != "aes-256-gcm" {
		t.Fatalf("expected Encryption aes-256-gcm, got %q", feat.Encryption)
	}
	if len(feat.Transports) == 0 {
		t.Fatalf("expected non-empty Transports")
	}
}

func TestHysteria2HandshakeEncryptedEcho(t *testing.T) {
	buf := &bytes.Buffer{}
	s := &memStream{ReadWriter: buf}

	p := &Hysteria2Protocol{}
	cfg := &protoiface.Config{
		Token: "test-token",
		Raw:   map[string]any{},
	}

	session, err := p.Handshake(s, cfg)
	if err != nil {
		t.Fatalf("Handshake failed: %v", err)
	}

	plain := []byte("hello-hysteria2")
	if _, err := session.Write(plain); err != nil {
		t.Fatalf("write via session failed: %v", err)
	}

	// На стороне "сервера" разворачиваем ту же сессию поверх того же буфера.
	serverStream := &memStream{ReadWriter: buf}
	serverSess, err := p.Handshake(serverStream, cfg)
	if err != nil {
		t.Fatalf("server Handshake failed: %v", err)
	}

	// Сервер читает зашифрованный кадр, расшифровывает его и эхо-отправляет обратно.
	readBuf := make([]byte, len(plain))
	if _, err := io.ReadFull(serverSess, readBuf); err != nil {
		t.Fatalf("server ReadFull failed: %v", err)
	}
	if !bytes.Equal(readBuf, plain) {
		t.Fatalf("server received mismatch: expected %q, got %q", string(plain), string(readBuf))
	}
	if _, err := serverSess.Write(readBuf); err != nil {
		t.Fatalf("server echo Write failed: %v", err)
	}

	// Клиент снова читает из своей сессии.
	clientRead := make([]byte, len(plain))
	if _, err := io.ReadFull(session, clientRead); err != nil {
		t.Fatalf("client ReadFull failed: %v", err)
	}
	if !bytes.Equal(clientRead, plain) {
		t.Fatalf("echo mismatch: expected %q, got %q", string(plain), string(clientRead))
	}
}

