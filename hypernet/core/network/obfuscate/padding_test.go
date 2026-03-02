package obfuscate

import (
	"bytes"
	"io"
	"testing"
)

// doublePipe связывает два направления: client out -> server in, server out -> client in.
func doublePipe() (client, server io.ReadWriteCloser) {
	aR, aW := io.Pipe()
	bR, bW := io.Pipe()
	client = &bidirPipe{in: bR, out: aW}
	server = &bidirPipe{in: aR, out: bW}
	return client, server
}

type bidirPipe struct {
	in  *io.PipeReader
	out *io.PipeWriter
}

func (b *bidirPipe) Read(p []byte) (n int, err error)  { return b.in.Read(p) }
func (b *bidirPipe) Write(p []byte) (n int, err error) { return b.out.Write(p) }
func (b *bidirPipe) Close() error {
	_ = b.in.Close()
	return b.out.Close()
}

func TestPaddingConnRoundtrip(t *testing.T) {
	clientRaw, serverRaw := doublePipe()
	client := NewPaddingConn(clientRaw, 0, 64)
	server := NewPaddingConn(serverRaw, 0, 64)
	defer client.Close()
	defer server.Close()

	msg := []byte("hello")
	go func() {
		_, _ = client.Write(msg)
		_ = client.Close()
	}()
	buf := make([]byte, 32)
	n, err := server.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(buf[:n], msg) {
		t.Fatalf("got %q", buf[:n])
	}
}

func TestPaddingConnUpTo512(t *testing.T) {
	clientRaw, serverRaw := doublePipe()
	// Паддинг до 512: задаём 400..600, nextPaddingLen ограничит max = 512
	client := NewPaddingConn(clientRaw, 400, 600)
	server := NewPaddingConn(serverRaw, 400, 600)
	defer client.Close()
	defer server.Close()

	msg := []byte("x")
	go func() {
		_, _ = client.Write(msg)
		_ = client.Close()
	}()
	buf := make([]byte, 16)
	n, err := server.Read(buf)
	if err != nil && err != io.EOF {
		t.Fatalf("read: %v", err)
	}
	if n != 1 || buf[0] != 'x' {
		t.Fatalf("got n=%d buf=%q", n, buf[:n])
	}
}

