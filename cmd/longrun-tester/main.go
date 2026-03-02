package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"hypernet-node/pkg/client"
	"hypernet-node/pkg/config"
	hypernode "hypernet-node/hypernet/core/network/node"
)

func main() {
	nodeAddr := flag.String("node", "", "proxy node multiaddr with /p2p/PeerID")
	target := flag.String("target", "", "upstream host:port for low-traffic test (e.g. 127.0.0.1:5201)")
	interval := flag.Duration("interval", 10*time.Second, "interval between small requests")
	total := flag.Duration("total", 24*time.Hour, "total test duration")
	flag.Parse()

	if *nodeAddr == "" || *target == "" {
		fmt.Fprintf(os.Stderr, "usage: longrun-tester -node <multiaddr> -target <host:port> [-interval 10s] [-total 24h]\n")
		os.Exit(2)
	}

	host, portStr, err := splitHostPort(*target)
	if err != nil {
		log.Fatalf("invalid target: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	node, err := hypernode.New(ctx, cfg)
	if err != nil {
		log.Fatalf("create node: %v", err)
	}
	defer node.Close()

	pc, err := client.NewProxyClient(node.Host, *nodeAddr, "")
	if err != nil {
		log.Fatalf("NewProxyClient: %v", err)
	}

	deadline := time.Now().Add(*total)
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	log.Printf("longrun-tester started, node=%s, target=%s, interval=%v, total=%v", *nodeAddr, *target, *interval, *total)

	for {
		select {
		case <-ctx.Done():
			log.Printf("context cancelled, exiting")
			return
		case now := <-ticker.C:
			if now.After(deadline) {
				log.Printf("reached total duration, exiting")
				return
			}
			if err := doOnce(ctx, pc, host, port); err != nil {
				log.Printf("low-traffic request failed: %v", err)
			} else {
				log.Printf("low-traffic request succeeded")
			}
		}
	}
}

func splitHostPort(addr string) (string, string, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", "", err
	}
	return host, port, nil
}

func doOnce(ctx context.Context, pc *client.ProxyClient, host string, port int) error {
	stream, err := pc.DialTCP(ctx, host, port)
	if err != nil {
		return fmt.Errorf("DialTCP: %w", err)
	}
	defer stream.Close()

	payload := make([]byte, 32)
	if _, err := rand.Read(payload); err != nil {
		return err
	}
	if _, err := stream.Write(payload); err != nil {
		return err
	}
	buf := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, buf); err != nil {
		return err
	}
	return nil
}

