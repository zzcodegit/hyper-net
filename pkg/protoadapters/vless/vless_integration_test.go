package vless

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

// echoProtoID — вспомогательный протокол для проверки прохода трафика через vless-адаптер.
const echoProtoID = "/hypernet/vless-echo/1.0.0"

func newTestHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	return h
}

func connectHosts(t *testing.T, a, b host.Host) {
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
	if err := a.Connect(context.Background(), *info); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

// TestVLESSNegotiationAndEcho проверяет, что:
// 1) negotiation-протокол выбирает "vless" как общий;
// 2) поверх libp2p-потока, обёрнутого vless-ручным рукопожатием, трафик эхоится.
func TestVLESSNegotiationAndEcho(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverHost := newTestHost(t)
	defer serverHost.Close()
	clientHost := newTestHost(t)
	defer clientHost.Close()

	// Менеджеры протоколов: на этом уровне нам важно лишь, что выбран именно "vless".
	serverMgr := protomanager.NewManager([]string{ProtocolName})
	clientMgr := protomanager.NewManager([]string{ProtocolName})

	serverMgr.Register(&VLESSProtocol{})
	clientMgr.Register(&VLESSProtocol{})

	// Обработчик negotiation-протокола на сервере.
	serverHost.SetStreamHandler(protonegotiate.ProtocolID, func(s network.Stream) {
		_, _ = protonegotiate.HandleStream(ctx, s, serverMgr)
	})

	// Echo-хендлер, который ожидает, что поверх потока уже прошёл vless-handshake.
	serverHost.SetStreamHandler(echoProtoID, func(s network.Stream) {
		defer s.Close()
		// На стороне сервера выполняем Handshake в роли "server".
		adapter := &VLESSProtocol{}
		cfg := &protoiface.Config{
			Token:   "test-token",
			Timeout: 2 * time.Second,
			Raw:     map[string]any{cfgKeyRole: roleServer},
		}
		session, err := adapter.Handshake(s, cfg)
		if err != nil {
			// Рукопожатие не удалось – просто закрываем поток.
			return
		}
		// Эхо-проксирование.
		_, _ = io.Copy(session, session)
	})

	connectHosts(t, clientHost, serverHost)

	// Клиент узнаёт, что общий протокол — "vless".
	chosen, _, err := protonegotiate.ClientNegotiate(ctx, clientHost, serverHost.ID(), clientMgr)
	if err != nil {
		t.Fatalf("ClientNegotiate failed: %v", err)
	}
	if chosen != ProtocolName {
		t.Fatalf("expected chosen protocol %q, got %q", ProtocolName, chosen)
	}

	// Открываем echo-поток и выполняем vless-рукопожатие в роли клиента.
	stream, err := clientHost.NewStream(ctx, serverHost.ID(), echoProtoID)
	if err != nil {
		t.Fatalf("NewStream(echoProtoID): %v", err)
	}
	defer stream.Close()

	adapter := &VLESSProtocol{}
	clientCfg := &protoiface.Config{
		Token:   "test-token",
		Timeout: 2 * time.Second,
		Raw:     map[string]any{cfgKeyRole: roleClient},
	}
	session, err := adapter.Handshake(stream, clientCfg)
	if err != nil {
		t.Fatalf("vless client handshake failed: %v", err)
	}

	msg := []byte("hello-vless")
	if _, err := session.Write(msg); err != nil {
		t.Fatalf("write via vless session: %v", err)
	}

	buf := make([]byte, len(msg))
	if _, err := io.ReadFull(session, buf); err != nil {
		t.Fatalf("read via vless session: %v", err)
	}
	if !bytes.Equal(buf, msg) {
		t.Fatalf("echo mismatch: expected %q, got %q", string(msg), string(buf))
	}
}

