package plain

import (
	"bytes"
	"io"
	"net"
	"testing"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

type rwc struct {
	io.ReadWriter
}

func (r *rwc) Close() error { return nil }

func TestPlain_NameAndFeatures(t *testing.T) {
	p := &PlainProtocol{}
	if p.Name() != ProtocolName {
		t.Fatalf("unexpected name: %s", p.Name())
	}
	f := p.Features()
	if len(f.Transports) == 0 || f.Transports[0] != "tcp" {
		t.Fatalf("unexpected transports: %#v", f.Transports)
	}
	if f.Encryption != "none" {
		t.Fatalf("unexpected encryption: %s", f.Encryption)
	}
}

func TestPlain_HandshakePassthrough(t *testing.T) {
	buf := &bytes.Buffer{}
	stream := &rwc{ReadWriter: buf}

	p := &PlainProtocol{}
	cfg := &protoiface.Config{
		Token: "ignored",
		Raw:   map[string]any{obfsEnabledKey: false},
	}

	sess, err := p.Handshake(stream, cfg)
	if err != nil {
		t.Fatalf("Handshake error: %v", err)
	}

	if _, err := sess.Write([]byte("hello")); err != nil {
		t.Fatalf("write via session: %v", err)
	}
	if got := buf.String(); got != "hello" {
		t.Fatalf("expected 'hello', got %q", got)
	}
}

func TestPlain_WrapPassthrough(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	p := &PlainProtocol{}
	cfg := &protoiface.Config{Raw: map[string]any{obfsEnabledKey: false}}

	wrapped, err := p.Wrap(client, cfg)
	if err != nil {
		t.Fatalf("Wrap error: %v", err)
	}

	go func() {
		_, _ = server.Write([]byte("hi"))
	}()

	buf := make([]byte, 2)
	if _, err := wrapped.Read(buf); err != nil {
		t.Fatalf("read via wrapped conn: %v", err)
	}
	if string(buf) != "hi" {
		t.Fatalf("expected 'hi', got %q", string(buf))
	}
}

