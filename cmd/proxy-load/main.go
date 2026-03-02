package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"hypernet-node/pkg/client"
	"hypernet-node/pkg/config"
	hypernode "hypernet-node/hypernet/core/network/node"
)

func main() {
	nodeAddr := flag.String("node", "", "proxy node multiaddr with /p2p/PeerID")
	target := flag.String("target", "", "upstream echo server address host:port (will be reached via proxy)")
	levels := flag.String("levels", "100,500,1000", "comma-separated list of concurrent connection levels")
	duration := flag.Int("duration", 10, "iperf-like test duration per level in seconds")
	flag.Parse()

	if *nodeAddr == "" || *target == "" {
		fmt.Fprintf(os.Stderr, "usage: proxy-load -node <multiaddr> -target <host:port> [-levels 100,500,1000] [-duration 10]\n")
		os.Exit(2)
	}

	host, portStr, err := splitHostPort(*target)
	if err != nil {
		log.Fatalf("invalid target: %v", err)
	}
	port, _ := strconv.Atoi(portStr)

	var connLevels []int
	for _, part := range strings.Split(*levels, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n <= 0 {
			log.Fatalf("invalid level %q", part)
		}
		connLevels = append(connLevels, n)
	}
	if len(connLevels) == 0 {
		log.Fatalf("no valid levels parsed from %q", *levels)
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

	pc, err := client.NewProxyClient(node.Host, *nodeAddr, "")
	if err != nil {
		log.Fatalf("NewProxyClient: %v", err)
	}

	for _, level := range connLevels {
		log.Printf("=== proxy-load: level %d concurrent connections, duration %ds ===", level, *duration)
		if err := runLevel(ctx, pc, host, port, level, time.Duration(*duration)*time.Second); err != nil {
			log.Printf("level %d FAILED: %v", level, err)
		} else {
			log.Printf("level %d DONE", level)
		}
		printSelfStats()
	}
}

func runLevel(ctx context.Context, pc *client.ProxyClient, host string, port, level int, d time.Duration) error {
	var wg sync.WaitGroup
	errCh := make(chan error, level)

	deadline := time.Now().Add(d)

	for i := 0; i < level; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for time.Now().Before(deadline) {
				stream, err := pc.DialTCP(ctx, host, port)
				if err != nil {
					errCh <- fmt.Errorf("conn %d: dial: %w", id, err)
					return
				}

				if _, err := io.WriteString(stream, "hello"); err != nil {
					_ = stream.Close()
					errCh <- fmt.Errorf("conn %d: write: %w", id, err)
					return
				}
				buf := make([]byte, 5)
				if _, err := io.ReadFull(stream, buf); err != nil {
					_ = stream.Close()
					errCh <- fmt.Errorf("conn %d: read: %w", id, err)
					return
				}
				_ = stream.Close()

				if string(buf) != "hello" {
					errCh <- fmt.Errorf("conn %d: expected echo 'hello', got %q", id, string(buf))
					return
				}
			}
			errCh <- nil
		}(i)
	}

	go func() {
		wg.Wait()
		close(errCh)
	}()

	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func splitHostPort(addr string) (string, string, error) {
	host, port, err := netSplitHostPort(addr)
	if err != nil {
		return "", "", err
	}
	return host, port, nil
}

func netSplitHostPort(addr string) (string, string, error) {
	// thin wrapper to avoid importing net in too many helpers
	h, p, err := netSplit(addr)
	return h, p, err
}

func netSplit(addr string) (string, string, error) {
	parts := strings.Split(addr, ":")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid host:port %q", addr)
	}
	return parts[0], parts[1], nil
}

func printSelfStats() {
	f, err := os.Open("/proc/self/status")
	if err != nil {
		log.Printf("cannot read /proc/self/status: %v", err)
		return
	}
	defer f.Close()

	r := bufio.NewScanner(f)
	var vmRSS, fdSize string
	for r.Scan() {
		line := r.Text()
		if strings.HasPrefix(line, "VmRSS:") {
			vmRSS = strings.TrimSpace(line)
		}
		if strings.HasPrefix(line, "FDSize:") {
			fdSize = strings.TrimSpace(line)
		}
	}
	if vmRSS != "" || fdSize != "" {
		log.Printf("self stats: %s, %s", vmRSS, fdSize)
	}
}

