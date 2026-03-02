package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"hypernet-node/pkg/client"
	"hypernet-node/pkg/config"
	hypernode "hypernet-node/hypernet/core/network/node"
)

func main() {
	nodeAddr := flag.String("node", "", "proxy node multiaddr with /p2p/PeerID (e.g. /ip4/127.0.0.1/tcp/4001/p2p/...)")
	listen := flag.String("listen", "127.0.0.1:5202", "local address to listen on for plain TCP (for clients like iperf3)")
	upstream := flag.String("upstream", "", "upstream target in host:port form (e.g. 127.0.0.1:5201)")
	flag.Parse()

	if *nodeAddr == "" || *upstream == "" {
		fmt.Fprintf(os.Stderr, "usage: proxy-forwarder -node <multiaddr> -listen <addr> -upstream <host:port>\n")
		os.Exit(2)
	}

	upHost, upPort, err := splitHostPort(*upstream)
	if err != nil {
		log.Fatalf("invalid upstream: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Вспомогательная нода для форвардера: отдельный ключ, произвольный порт.
	cfg := &config.Config{
		Port:    0,                      // OS выберет свободный порт
		KeyPath: "proxy-forwarder.key",  // не конфликтовать с основными нодами
	}

	node, err := hypernode.New(ctx, cfg)
	if err != nil {
		log.Fatalf("create node: %v", err)
	}
	defer node.Close()

	vlessToken := os.Getenv("HYPERNET_VLESS_TOKEN")
	pc, err := client.NewProxyClient(node.Host, *nodeAddr, vlessToken)
	if err != nil {
		log.Fatalf("NewProxyClient: %v", err)
	}

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen %s: %v", *listen, err)
	}
	log.Printf("proxy-forwarder listening on %s, upstream %s via %s", *listen, *upstream, *nodeAddr)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("accept error: %v", err)
			continue
		}

		go handleConn(ctx, conn, pc, upHost, upPort)
	}
}

func splitHostPort(addr string) (string, int, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		// попробовать добавить : если не указан.
		if !strings.Contains(addr, ":") {
			return "", 0, err
		}
		return "", 0, err
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		return "", 0, err
	}
	return host, port, nil
}

func handleConn(ctx context.Context, conn net.Conn, pc *client.ProxyClient, upHost string, upPort int) {
	defer conn.Close()

	start := time.Now()

	stream, err := pc.DialTCPWithFallback(ctx, upHost, upPort, 0)
	if err != nil {
		log.Printf("DialTCP via proxy failed: %v", err)
		return
	}
	defer stream.Close()

	lat, errs := pc.Stats()
	proto := pc.ChosenProtocol()
	log.Printf("proxy-forwarder: opened stream to %s:%d via protocol=%q (dialLatency=%s, errorCount=%d)",
		upHost, upPort, proto, lat, errs)

	// двунаправленный копирование
	errCh := make(chan error, 2)

	go func() {
		_, err := io.Copy(stream, conn)
		errCh <- err
	}()
	go func() {
		_, err := io.Copy(conn, stream)
		errCh <- err
	}()

	// ждём первого завершения
	select {
	case <-ctx.Done():
	case <-errCh:
	case <-errCh:
	}

	elapsed := time.Since(start)
	latFinal, errsFinal := pc.Stats()
	log.Printf("proxy-forwarder: connection to %s:%d closed, duration=%s, lastDialLatency=%s, totalErrors=%d",
		upHost, upPort, elapsed, latFinal, errsFinal)
}

