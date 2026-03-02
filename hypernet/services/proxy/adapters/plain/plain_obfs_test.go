package plain

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

// TestPlainObfs_WrapCanBeDisabled гарантирует, что обфускацию можно явно отключить,
// и при этом семантика echo сохраняется.
func TestPlainObfs_WrapCanBeDisabled(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	p := &PlainProtocol{}
	cfg := DefaultConfig()
	cfg.Raw[obfsEnabledKey] = false // явное отключение обфускации

	sess, err := p.Wrap(c1, cfg)
	if err != nil {
		t.Fatalf("Wrap failed: %v", err)
	}

	go func() {
		defer c2.Close()
		_, _ = io.Copy(c2, c2)
	}()

	msg := []byte("hello-plain-no-obfs")
	if _, err := sess.Write(msg); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(sess, buf); err != nil {
		t.Fatalf("ReadFull failed: %v", err)
	}
	if !bytes.Equal(buf, msg) {
		t.Fatalf("echo mismatch: expected %q, got %q", string(msg), string(buf))
	}
}

// TestPlainObfs_WrapWithPaddingEcho проверяет, что при включённой обфускации
// (паддинг) двусторонний обмен через два обфусцированных конца остаётся
// корректным для пользователя.
func TestPlainObfs_WrapWithPaddingEcho(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()

	p := &PlainProtocol{}
	cfg := DefaultConfig()
	cfg.Raw[obfsEnabledKey] = true
	cfg.Raw[obfsMaxPaddingKey] = 16
	cfg.Raw[obfsDummyMaxMsKey] = 0 // отключаем dummy в тестах, чтобы не блокировать на pipe

	// Оборачиваем оба конца, чтобы они понимали фреймовый формат.
	sessA, err := p.Wrap(c1, cfg)
	if err != nil {
		t.Fatalf("Wrap A failed: %v", err)
	}
	sessB, err := p.Wrap(c2, cfg)
	if err != nil {
		t.Fatalf("Wrap B failed: %v", err)
	}

	// На стороне B запускаем echo-сервер.
	go func() {
		defer sessB.Close()
		_, _ = io.Copy(sessB, sessB)
	}()

	msg := []byte("hello-obfs-padding")
	if _, err := sessA.Write(msg); err != nil {
		t.Fatalf("Write via sessA failed: %v", err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(sessA, buf); err != nil {
		t.Fatalf("ReadFull via sessA failed: %v", err)
	}
	if !bytes.Equal(buf, msg) {
		t.Fatalf("echo mismatch: expected %q, got %q", string(msg), string(buf))
	}
}

// TestPlainObfs_HandshakeUsesObfsSession проверяет, что Handshake возвращает
// именно обфусцирующую обёртку при наличии настроек обфускации.
func TestPlainObfs_HandshakeUsesObfsSession(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()

	p := &PlainProtocol{}
	cfg := &protoiface.Config{
		Token:   "",
		Timeout: time.Second,
		Raw: map[string]any{
			obfsEnabledKey:    true,
			obfsMaxPaddingKey: 8,
			obfsDummyMaxMsKey: 0, // без dummy, иначе runDummy блокируется на pipe
		},
	}
	sess, err := p.Handshake(pipeDuplex{r: r, w: w}, cfg)
	if err != nil {
		t.Fatalf("Handshake failed: %v", err)
	}
	defer sess.Close()
	if _, ok := sess.(*obfsSession); !ok {
		t.Fatalf("expected Handshake to return *obfsSession when obfuscation enabled, got %T", sess)
	}
}

// pipeDuplex адаптирует io.PipeReader/io.PipeWriter к io.ReadWriteCloser.
type pipeDuplex struct {
	r *io.PipeReader
	w *io.PipeWriter
}

func (p pipeDuplex) Read(b []byte) (int, error)  { return p.r.Read(b) }
func (p pipeDuplex) Write(b []byte) (int, error) { return p.w.Write(b) }
func (p pipeDuplex) Close() error {
	_ = p.r.Close()
	return p.w.Close()
}

