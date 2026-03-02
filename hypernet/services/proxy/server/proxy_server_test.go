package proxy

import (
	"bytes"
	"context"
	"io"
	"sync"
	"testing"
	"time"

	metrics "hypernet-node/hypernet/services/proxy/metrics"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

func TestRememberChosenProtocol_LastChosenProtocol(t *testing.T) {
	srv := NewServer()
	priv1, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key 1: %v", err)
	}
	priv2, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key 2: %v", err)
	}
	id1, err := peer.IDFromPrivateKey(priv1)
	if err != nil {
		t.Fatalf("peer id 1: %v", err)
	}
	id2, err := peer.IDFromPrivateKey(priv2)
	if err != nil {
		t.Fatalf("peer id 2: %v", err)
	}

	// Initially empty
	if got := srv.lastChosenProtocol(id1); got != "" {
		t.Errorf("lastChosenProtocol(id1) = %q, want empty", got)
	}

	srv.RememberChosenProtocol(id1, "vless")
	if got := srv.lastChosenProtocol(id1); got != "vless" {
		t.Errorf("lastChosenProtocol(id1) = %q, want vless", got)
	}

	srv.RememberChosenProtocol(id2, "trojan")
	if got := srv.lastChosenProtocol(id2); got != "trojan" {
		t.Errorf("lastChosenProtocol(id2) = %q, want trojan", got)
	}
	if got := srv.lastChosenProtocol(id1); got != "vless" {
		t.Errorf("lastChosenProtocol(id1) after id2 = %q, want vless", got)
	}

	// Empty string clears
	srv.RememberChosenProtocol(id1, "")
	if got := srv.lastChosenProtocol(id1); got != "" {
		t.Errorf("lastChosenProtocol(id1) after clear = %q, want empty", got)
	}
}

// Граничные случаи RememberChosenProtocol: перезапись, неизвестный peer, пустая строка.
func TestRememberChosenProtocol_OverwriteAndUnknownPeer(t *testing.T) {
	srv := NewServer()
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}

	// Неизвестный peer — пустая строка
	if got := srv.lastChosenProtocol(id); got != "" {
		t.Errorf("unknown peer: got %q, want empty", got)
	}

	// Запись и перезапись
	srv.RememberChosenProtocol(id, "vless")
	srv.RememberChosenProtocol(id, "trojan")
	if got := srv.lastChosenProtocol(id); got != "trojan" {
		t.Errorf("after overwrite: got %q, want trojan", got)
	}

	// Очистка пустой строкой
	srv.RememberChosenProtocol(id, "")
	if got := srv.lastChosenProtocol(id); got != "" {
		t.Errorf("after clear: got %q, want empty", got)
	}
}

// Конкурентные вызовы RememberChosenProtocol и lastChosenProtocol не должны паниковать.
func TestRememberChosenProtocol_Concurrent(t *testing.T) {
	srv := NewServer()
	ids := make([]peer.ID, 8)
	for i := range ids {
		priv, _, _ := crypto.GenerateEd25519Key(nil)
		id, _ := peer.IDFromPrivateKey(priv)
		ids[i] = id
	}
	protos := []string{"vless", "trojan", "plain", ""}
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(j int) {
			defer wg.Done()
			id := ids[j%len(ids)]
			proto := protos[j%len(protos)]
			srv.RememberChosenProtocol(id, proto)
			_ = srv.lastChosenProtocol(id)
		}(i)
	}
	wg.Wait()
}

func TestPrefixConn_ReadThenRest(t *testing.T) {
	prefix := []byte("PREFIX")
	restContent := []byte("rest-data")
	rest := &rwcBuffer{buf: bytes.NewBuffer(restContent)}
	pc := &prefixConn{prefix: prefix, rest: rest}

	// Read first: should get prefix
	out := make([]byte, 20)
	n, err := pc.Read(out)
	if err != nil {
		t.Fatalf("Read prefix: %v", err)
	}
	if n != len(prefix) || !bytes.Equal(out[:n], prefix) {
		t.Errorf("Read prefix: got %q, want %q", out[:n], prefix)
	}

	// Read again: should get rest
	n, err = pc.Read(out)
	if err != nil && err != io.EOF {
		t.Fatalf("Read rest: %v", err)
	}
	if !bytes.Equal(out[:n], restContent) {
		t.Errorf("Read rest: got %q, want %q", out[:n], restContent)
	}
}

func TestPrefixConn_WritePassthrough(t *testing.T) {
	rest := &rwcBuffer{buf: &bytes.Buffer{}}
	pc := &prefixConn{prefix: []byte("x"), rest: rest}
	data := []byte("hello")
	n, err := pc.Write(data)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(data) {
		t.Errorf("Write: n = %d, want %d", n, len(data))
	}
	if rest.buf.String() != "hello" {
		t.Errorf("rest buffer = %q, want hello", rest.buf.String())
	}
}

// rwcBuffer implements io.ReadWriteCloser over a bytes.Buffer (read) and bytes.Buffer (write).
type rwcBuffer struct {
	buf *bytes.Buffer
}

func (r *rwcBuffer) Read(p []byte) (n int, err error) { return r.buf.Read(p) }
func (r *rwcBuffer) Write(p []byte) (n int, err error) { return r.buf.Write(p) }
func (r *rwcBuffer) Close() error                     { return nil }

// TestBadRequestErrFormat проверяет, что при невалидном запросе ParseRequest возвращает
// ошибку с текстом, который handleProxyStream отправляет как "ERR "+err.Error()+"\n".
func TestBadRequestErrFormat(t *testing.T) {
	_, err := ParseRequest("XYZ example.com 80\n")
	if err == nil {
		t.Fatal("ParseRequest(XYZ ...) should fail")
	}
	errStr := err.Error()
	if errStr == "" {
		t.Error("error message should not be empty")
	}
	// handleProxyStream пишет "ERR "+err.Error()+"\n"
	if len(errStr) > 200 {
		t.Errorf("error message too long for wire: %d bytes", len(errStr))
	}
}

func TestSetNodeRegion_DoesNotPanic(t *testing.T) {
	srv := NewServer()
	srv.SetNodeRegion("  eu  ")
	srv.SetNodeRegion("")
	srv.SetNodeRegion("us-east-1")
}

// TestRunQualityLoop_ExitsWhenMetricsNil проверяет, что runQualityLoop сразу выходит при s.metrics == nil.
func TestRunQualityLoop_ExitsWhenMetricsNil(t *testing.T) {
	srv := NewServer()
	// metrics and host are nil
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	close(done)
	priv, _, err := crypto.GenerateEd25519Key(nil)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	id, err := peer.IDFromPrivateKey(priv)
	if err != nil {
		t.Fatalf("peer id: %v", err)
	}
	srv.runQualityLoop(ctx, "sid", id, &sessionCounters{}, time.Second, done)
}

// SetMetricsStore/SetMetricsQualityInterval с nil не должны паниковать.
func TestServer_SetMetrics_DoesNotPanic(t *testing.T) {
	srv := NewServer()
	srv.SetMetricsStore(nil)
	srv.SetMetricsQualityInterval(0)
	srv.SetMetricsQualityInterval(time.Minute)
}

// С реальным Store сеттеры не паникуют; StartSession/EndSession проверяются интеграционно.
func TestServer_SetMetricsStore_RealStore(t *testing.T) {
	store, err := metrics.NewStore(":memory:", 100*time.Millisecond)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	defer store.Close()
	srv := NewServer()
	srv.SetMetricsStore(store)
	srv.SetMetricsQualityInterval(time.Second)
	// handleProxyStream с записью в Store покрыт интеграционными тестами (pkg/client, e2e).
}

