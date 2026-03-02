package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"hypernet-node/pkg/client"
	"hypernet-node/pkg/config"
	hypernode "hypernet-node/hypernet/core/network/node"
)

func main() {
	nodeAddr := flag.String("node", "", "proxy node multiaddr with /p2p/PeerID (e.g. /ip4/127.0.0.1/tcp/4001/p2p/...)")
	megabytes := flag.Int("megabytes", 512, "amount of data to send through proxy tunnel (in MiB)")
	gateway := flag.String("gateway", "172.17.0.1", "host address as seen from Docker network for connecting back to this machine")
	flag.Parse()

	if *nodeAddr == "" {
		fmt.Fprintf(os.Stderr, "usage: proxy-bench -node <multiaddr> [-megabytes N] [-gateway 172.17.0.1]\n")
		os.Exit(2)
	}
	if *megabytes <= 0 {
		log.Fatalf("invalid megabytes: %d", *megabytes)
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	node, err := hypernode.New(ctx, cfg)
	if err != nil {
		log.Fatalf("create node: %v", err)
	}
	defer node.Close()

	// Локальный TCP‑сервер, играющий роль upstream‑эхо/синка.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		log.Fatalf("listen upstream: %v", err)
	}
	defer ln.Close()

	upPort := ln.Addr().(*net.TCPAddr).Port
	upHost := *gateway

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Просто сливаем всё, что приходит, чтобы не упираться в echo‑логику.
		_, _ = io.Copy(io.Discard, conn)
	}()

	pc, err := client.NewProxyClient(node.Host, *nodeAddr, "")
	if err != nil {
		log.Fatalf("NewProxyClient: %v", err)
	}

	stream, err := pc.DialTCPWithFallback(ctx, upHost, upPort, 2*time.Second)
	if err != nil {
		log.Fatalf("DialTCPWithFallback: %v", err)
	}
	defer stream.Close()

	totalBytes := int64(*megabytes) * 1024 * 1024
	buf := make([]byte, 64*1024)

	start := time.Now()
	var sent int64
	for sent < totalBytes {
		n, err := stream.Write(buf)
		if err != nil {
			log.Fatalf("write failed after %d bytes: %v", sent, err)
		}
		sent += int64(n)
	}
	elapsed := time.Since(start).Seconds()
	if elapsed <= 0 {
		elapsed = 0.000001
	}

	mbits := (float64(sent) * 8.0) / (1024.0 * 1024.0) / elapsed

	fmt.Printf("proxy-bench: bytes=%d duration=%.3fs throughput_mbit_per_sec=%.2f\n", sent, elapsed, mbits)
}

