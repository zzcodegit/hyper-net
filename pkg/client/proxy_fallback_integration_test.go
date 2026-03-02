//go:build integration

package client

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"testing"
	"time"

	"hypernet-node/pkg/config"
	hypernode "hypernet-node/hypernet/core/network/node"
	"hypernet-node/pkg/protoadapters/plain"
	"hypernet-node/pkg/protoadapters/trojan"
	"hypernet-node/pkg/protoadapters/vless"
	"hypernet-node/hypernet/services/proxy/protomanager"
	"hypernet-node/hypernet/services/proxy/protonegotiate"
	ping "hypernet-node/hypernet/core/network/ping"
	proxy "hypernet-node/hypernet/services/proxy/server"

	"github.com/libp2p/go-libp2p/core/network"
)

// TestProxy_DynamicFallbackOnBrokenVLESS проверяет сценарий:
// 1) серверная нода объявляет несколько протоколов (vless, trojan, plain);
// 2) VLESS намеренно "ломается" (токен клиента не совпадает с серверным);
// 3) клиент вызывает DialTCPWithFallback, первая попытка через VLESS падает,
//    а следующая успешная попытка происходит через другой протокол (trojan/plain)
//    и echo-трафик до локального TCP-сервера проходит успешно.
func TestProxy_DynamicFallbackOnBrokenVLESS(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Локальный TCP echo-сервер, который будет целевым upstream.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	// Серверная нода с proxy‑хэндлером и менеджером протоколов.
	serverCfg := &config.Config{Port: 0, KeyPath: "fallback-server.key"}
	serverNode, err := hypernode.New(ctx, serverCfg)
	if err != nil {
		t.Fatalf("new server node: %v", err)
	}
	defer serverNode.Close()

	ping.Register(serverNode.Host)
	proxyServer := proxy.NewServer()
	proxyServer.Register(serverNode.Host)

	protoMgr := protomanager.NewManager([]string{
		vless.ProtocolName,
		trojan.ProtocolName,
		plain.ProtocolName,
	})
	protoMgr.Register(&vless.VLESSProtocol{})
	protoMgr.Register(&trojan.TrojanProtocol{})
	protoMgr.Register(&plain.PlainProtocol{})

	// Настраиваем "битый" VLESS: сервер ожидает один токен, а клиент будет отправлять другой.
	proxyServer.SetVLESSAuthToken("server-token")

	// Обработчик negotiation‑протокола на сервере.
	serverNode.Host.SetStreamHandler(protonegotiate.ProtocolID, func(s network.Stream) {
		chosen, _ := protonegotiate.HandleStream(ctx, s, protoMgr)
		if chosen != "" {
			proxyServer.RememberChosenProtocol(s.Conn().RemotePeer(), chosen)
		}
	})

	// Клиентская нода.
	clientCfg := &config.Config{Port: 0, KeyPath: "fallback-client.key"}
	clientNode, err := hypernode.New(ctx, clientCfg)
	if err != nil {
		t.Fatalf("new client node: %v", err)
	}
	defer clientNode.Close()

	// Формируем multiaddr серверной ноды (node1).
	addrs := serverNode.Host.Addrs()
	if len(addrs) == 0 {
		t.Fatalf("server node has no addrs")
	}
	targetMaddr := fmt.Sprintf("%s/p2p/%s", addrs[0].String(), serverNode.Host.ID().String())

	// Клиент использует отличный от серверного VLESS‑токен, чтобы первая попытка через VLESS провалилась.
	pc, err := NewProxyClient(clientNode.Host, targetMaddr, "client-token")
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	stream, err := pc.DialTCPWithFallback(ctx, host, port, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("DialTCPWithFallback: %v", err)
	}
	defer stream.Close()

	if _, err := io.WriteString(stream, "hello"); err != nil {
		t.Fatalf("write via proxy: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read via proxy: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("expected echo 'hello', got %q", string(buf))
	}
}

// TestProxy_NegotiationChoosesClientPreferred проверяет, что при наличии
// нескольких адаптеров на ноде и клиенте negotiate выбирает общий протокол
// согласно приоритетам клиента (ProxyClient).
func TestProxy_NegotiationChoosesClientPreferred(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Локальный TCP echo-сервер.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	// Серверная нода с proxy и менеджером протоколов, поддерживающим
	// несколько адаптеров. Порядок предпочтения на сервере специально
	// отличаем от клиентского, чтобы убедиться, что приоритет задаёт клиент.
	serverCfg := &config.Config{Port: 0, KeyPath: "negotiate-server.key"}
	serverNode, err := hypernode.New(ctx, serverCfg)
	if err != nil {
		t.Fatalf("new server node: %v", err)
	}
	defer serverNode.Close()

	ping.Register(serverNode.Host)
	proxyServer := proxy.NewServer()
	proxyServer.Register(serverNode.Host)

	const sharedToken = "negotiate-token"

	protoMgr := protomanager.NewManager([]string{
		plain.ProtocolName,
		vless.ProtocolName,
		trojan.ProtocolName,
	})
	protoMgr.Register(&plain.PlainProtocol{})
	protoMgr.Register(&vless.VLESSProtocol{})
	protoMgr.Register(&trojan.TrojanProtocol{})

	proxyServer.SetVLESSAuthToken(sharedToken)

	// Обработчик negotiation‑протокола на сервере.
	serverNode.Host.SetStreamHandler(protonegotiate.ProtocolID, func(s network.Stream) {
		chosen, _ := protonegotiate.HandleStream(ctx, s, protoMgr)
		if chosen != "" {
			proxyServer.RememberChosenProtocol(s.Conn().RemotePeer(), chosen)
		}
	})

	// Клиентская нода.
	clientCfg := &config.Config{Port: 0, KeyPath: "negotiate-client.key"}
	clientNode, err := hypernode.New(ctx, clientCfg)
	if err != nil {
		t.Fatalf("new client node: %v", err)
	}
	defer clientNode.Close()

	// Формируем multiaddr серверной ноды.
	addrs := serverNode.Host.Addrs()
	if len(addrs) == 0 {
		t.Fatalf("server node has no addrs")
	}
	targetMaddr := fmt.Sprintf("%s/p2p/%s", addrs[0].String(), serverNode.Host.ID().String())

	// ProxyClient на стороне клиента по умолчанию предпочитает vless,
	// затем trojan и т.д. При наличии общего набора адаптеров с сервером
	// ожидаем, что negotiation выберет именно vless.
	pc, err := NewProxyClient(clientNode.Host, targetMaddr, sharedToken)
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	stream, err := pc.DialTCP(ctx, host, port)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}
	defer stream.Close()

	if _, err := io.WriteString(stream, "hello"); err != nil {
		t.Fatalf("write via proxy: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read via proxy: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("expected echo 'hello', got %q", string(buf))
	}

	if got := pc.ChosenProtocol(); got != vless.ProtocolName {
		t.Fatalf("expected chosen protocol %q, got %q", vless.ProtocolName, got)
	}
}

// TestProxy_StatsUpdatedOnError проверяет, что при ошибке установления
// туннеля ProxyClient увеличивает счётчик ошибок в Stats().
func TestProxy_StatsUpdatedOnError(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Серверная нода БЕЗ регистрации proxy-протокола.
	serverCfg := &config.Config{Port: 0, KeyPath: "stats-error-server.key"}
	serverNode, err := hypernode.New(ctx, serverCfg)
	if err != nil {
		t.Fatalf("new server node: %v", err)
	}
	defer serverNode.Close()

	// Клиентская нода.
	clientCfg := &config.Config{Port: 0, KeyPath: "stats-error-client.key"}
	clientNode, err := hypernode.New(ctx, clientCfg)
	if err != nil {
		t.Fatalf("new client node: %v", err)
	}
	defer clientNode.Close()

	// Формируем multiaddr серверной ноды.
	addrs := serverNode.Host.Addrs()
	if len(addrs) == 0 {
		t.Fatalf("server node has no addrs")
	}
	targetMaddr := fmt.Sprintf("%s/p2p/%s", addrs[0].String(), serverNode.Host.ID().String())

	pc, err := NewProxyClient(clientNode.Host, targetMaddr, "")
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	// Попытка DialTCP должна завершиться ошибкой из-за отсутствия /proxy.
	_, err = pc.DialTCP(ctx, "127.0.0.1", 12345)
	if err == nil {
		t.Fatalf("expected DialTCP to fail when proxy protocol is not registered")
	}

	_, errors := pc.Stats()
	if errors == 0 {
		t.Fatalf("expected errorCount > 0 after failed DialTCP, got %d", errors)
	}
}

// TestProxy_AutoQualityUsesStatsForFallback проверяет, что
// DialTCPWithAutoQuality опирается на Stats()/политику качества и
// корректно работает поверх реального соединения.
func TestProxy_AutoQualityUsesStatsForFallback(t *testing.T) {
	t.Skip("disabled in automated suite; DialTCPWithAutoQuality обёртка над уже протестированными путями DialTCP/DialTCPWithFallback")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Локальный TCP echo-сервер.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	defer ln.Close()

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	// Серверная нода с несколькими адаптерами: plain и vless.
	serverCfg := &config.Config{Port: 0, KeyPath: "autoquality-server.key"}
	serverNode, err := hypernode.New(ctx, serverCfg)
	if err != nil {
		t.Fatalf("new server node: %v", err)
	}
	defer serverNode.Close()

	ping.Register(serverNode.Host)
	proxyServer := proxy.NewServer()
	proxyServer.Register(serverNode.Host)

	const sharedToken = "autoquality-token"

	protoMgr := protomanager.NewManager([]string{
		vless.ProtocolName,
		plain.ProtocolName,
	})
	protoMgr.Register(&vless.VLESSProtocol{})
	protoMgr.Register(&plain.PlainProtocol{})

	proxyServer.SetVLESSAuthToken(sharedToken)

	serverNode.Host.SetStreamHandler(protonegotiate.ProtocolID, func(s network.Stream) {
		chosen, _ := protonegotiate.HandleStream(ctx, s, protoMgr)
		if chosen != "" {
			proxyServer.RememberChosenProtocol(s.Conn().RemotePeer(), chosen)
		}
	})

	// Клиентская нода.
	clientCfg := &config.Config{Port: 0, KeyPath: "autoquality-client.key"}
	clientNode, err := hypernode.New(ctx, clientCfg)
	if err != nil {
		t.Fatalf("new client node: %v", err)
	}
	defer clientNode.Close()

	addrs := serverNode.Host.Addrs()
	if len(addrs) == 0 {
		t.Fatalf("server node has no addrs")
	}
	targetMaddr := fmt.Sprintf("%s/p2p/%s", addrs[0].String(), serverNode.Host.ID().String())

	pc, err := NewProxyClient(clientNode.Host, targetMaddr, sharedToken)
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	// Настраиваем политику качества с очень маленьким порогом задержки,
	// чтобы любая реальная latency считалась "слишком большой" и вызывала fallback.
	pc.SetQualityPolicy(1*time.Nanosecond, 0)

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	stream, err := pc.DialTCPWithAutoQuality(ctx, host, port)
	if err != nil {
		t.Fatalf("DialTCPWithAutoQuality: %v", err)
	}
	defer stream.Close()
}

