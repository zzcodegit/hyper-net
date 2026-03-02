//go:build integration

package plain

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"
	"hypernet-node/hypernet/services/proxy/protomanager"
	"hypernet-node/hypernet/services/proxy/protonegotiate"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// echoProtoID — вспомогательный протокол для проверки прохода трафика через plain-адаптер.
const echoProtoID = "/hypernet/plain-echo/1.0.0"

func newTestHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	return h
}

func connectHosts(t *testing.T, ctx context.Context, a, b host.Host) {
	t.Helper()
	if len(b.Addrs()) == 0 {
		t.Fatalf("host b has no addrs")
	}
	maddr := b.Addrs()[0]
	full, err := multiaddr.NewMultiaddr(maddr.String() + "/p2p/" + b.ID().String())
	if err != nil {
		t.Fatalf("multiaddr: %v", err)
	}
	info, err := peer.AddrInfoFromP2pAddr(full)
	if err != nil {
		t.Fatalf("AddrInfoFromP2pAddr: %v", err)
	}
	if err := a.Connect(ctx, *info); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

// TestPlainNegotiationAndEcho проверяет, что:
// 1) negotiation-протокол успешно выбирает "plain" как общий;
// 2) поверх обычного libp2p-потока plain-адаптер пропускает данные до echo-сервера.
func TestPlainNegotiationAndEcho(t *testing.T) {
	// Короткий таймаут контекста и закрытие хостов в горутинах, чтобы на удалённом сервере не зависать (host.Close может долго ждать).
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	// Серверная и клиентская ноды libp2p.
	serverHost := newTestHost(t)
	defer func() { go serverHost.Close() }()
	clientHost := newTestHost(t)
	defer func() { go clientHost.Close() }()

	// Менеджеры протоколов с зарегистрированным plain-адаптером.
	serverMgr := protomanager.NewManager([]string{ProtocolName})
	clientMgr := protomanager.NewManager([]string{ProtocolName})

	plainAdapter := &PlainProtocol{}
	serverMgr.Register(plainAdapter)
	clientMgr.Register(plainAdapter)

	// На сервере регистрируем обработчик negotiation-протокола.
	serverHost.SetStreamHandler(protonegotiate.ProtocolID, func(s network.Stream) {
		_, _ = protonegotiate.HandleStream(ctx, s, serverMgr)
	})

	// Плюс простой echo-обработчик для тестового протокола.
	serverHost.SetStreamHandler(echoProtoID, func(s network.Stream) {
		defer s.Close()
		// echo: всё, что приходит, отправляем обратно.
		_, _ = io.Copy(s, s)
	})

	connectHosts(t, ctx, clientHost, serverHost)

	// Negotiation: клиент узнаёт, что доступен протокол "plain".
	chosen, _, err := protonegotiate.ClientNegotiate(ctx, clientHost, serverHost.ID(), clientMgr)
	if err != nil {
		t.Fatalf("ClientNegotiate failed: %v", err)
	}
	if chosen != ProtocolName {
		t.Fatalf("expected chosen protocol %q, got %q", ProtocolName, chosen)
	}

	// Открываем обычный libp2p-поток и оборачиваем его через plain-адаптер.
	stream, err := clientHost.NewStream(ctx, serverHost.ID(), echoProtoID)
	if err != nil {
		t.Fatalf("NewStream(echoProtoID): %v", err)
	}
	defer stream.Close()

	session, err := plainAdapter.Handshake(stream, &protoiface.Config{
		Token:   "",
		Timeout: 2 * time.Second,
		Raw:     nil,
	})
	if err != nil {
		t.Fatalf("plain Handshake failed: %v", err)
	}

	// Пишем в сессию и ждём echo-ответ.
	msg := []byte("hello-plain")
	if _, err := session.Write(msg); err != nil {
		t.Fatalf("write via session: %v", err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(session, buf); err != nil {
		t.Fatalf("read via session: %v", err)
	}
	if !bytes.Equal(buf, msg) {
		t.Fatalf("echo mismatch: expected %q, got %q", string(msg), string(buf))
	}
}

