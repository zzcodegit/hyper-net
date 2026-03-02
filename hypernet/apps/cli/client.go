package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"hypernet-node/pkg/client"
	"hypernet-node/pkg/config"
	"hypernet-node/hypernet/services/dns"
	hypernode "hypernet-node/hypernet/core/network/node"
)

// isMultiaddr возвращает true, если s похоже на multiaddr (начинается с /).
func isMultiaddr(s string) bool {
	return strings.HasPrefix(strings.TrimSpace(s), "/")
}

// Main contains the original hypernet-client CLI logic.
// It is invoked from cmd/client/main.go and preserves the existing behavior.
func Main() {
	// Подкоманда resolve: hypernet-client resolve <name>
	if len(os.Args) >= 2 && os.Args[1] == "resolve" {
		if len(os.Args) < 3 {
			fmt.Fprintf(os.Stderr, "usage: hypernet-client resolve <name>\n")
			os.Exit(2)
		}
		name := os.Args[2]
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if err := runResolve(ctx, name); err != nil {
			log.Fatalf("resolve: %v", err)
		}
		return
	}

	targetNode := flag.String("node", "", "target proxy node: multiaddr (/ip4/.../p2p/...) or DHT name")
	targetHost := flag.String("host", "", "target host (e.g. example.com)")
	targetPort := flag.Int("port", 0, "target port")
	useUDP := flag.Bool("udp", false, "use UDP instead of TCP")
	httpGet := flag.Bool("http-get", false, "perform simple HTTP GET / over the proxy and print response (TCP only)")
	flag.Parse()

	if *targetNode == "" || *targetHost == "" || *targetPort == 0 {
		fmt.Fprintf(os.Stderr, "usage: hypernet-client -node <multiaddr|name> -host <host> -port <port> [-udp] [-http-get]\n")
		fmt.Fprintf(os.Stderr, "       hypernet-client resolve <name>\n")
		os.Exit(2)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		cancel()
	}()

	// Если -node не похож на multiaddr (не начинается с /), считаем его DHT-именем и разрешаем.
	nodeAddr := *targetNode
	needDHT := nodeAddr != "" && !isMultiaddr(nodeAddr)

	// Конфиг только из env, без вызова config.Load(): там определяются флаги -port и др., уже заданные выше.
	cfg := clientConfigFromEnv()
	if needDHT {
		cfg.EnableDHT = true
		cfg.DHTMode = "client"
	}

	n, err := hypernode.New(ctx, cfg)
	if err != nil {
		log.Fatalf("create node: %v", err)
	}
	defer n.Close()

	var ns *dns.NameSystem
	if needDHT && n.DHT != nil {
		ns = dns.NewNameSystem(n.DHT, n.Host)
		addrs, err := ns.Resolve(ctx, nodeAddr)
		if err != nil {
			log.Fatalf("dns resolve %q: %v", nodeAddr, err)
		}
		nodeAddr = addrs[0].String()
		log.Printf("resolved %q -> %s", *targetNode, nodeAddr)
	}

	vlessToken := os.Getenv("HYPERNET_VLESS_TOKEN")
	pc, err := client.NewProxyClient(n.Host, nodeAddr, vlessToken)
	if err != nil {
		log.Fatalf("new proxy client: %v", err)
	}
	if ns != nil {
		pc.SetNameSystem(ns)
	}
	if os.Getenv("HYPERNET_USE_UTLS") == "1" || strings.ToLower(os.Getenv("HYPERNET_USE_UTLS")) == "true" {
		pc.UseUTLS = true
	}

	if *useUDP && *httpGet {
		log.Fatalf("-http-get поддерживается только для TCP")
	}

	var stream io.ReadWriteCloser
	var errDial error
	if *useUDP {
		stream, errDial = pc.DialUDP(ctx, *targetHost, *targetPort)
	} else {
		// Для TCP используем версию с fallback по протоколам, чтобы при
		// проблемах с предпочтительным адаптером можно было автоматически
		// переключиться на другой.
		stream, errDial = pc.DialTCPWithFallback(ctx, *targetHost, *targetPort, 2*time.Second)
	}
	if errDial != nil {
		log.Fatalf("dial via proxy: %v", errDial)
	}
	defer stream.Close()

	if *httpGet {
		req := fmt.Sprintf("GET / HTTP/1.0\r\nHost: %s\r\nConnection: close\r\n\r\n", *targetHost)
		if _, err := io.WriteString(stream, req); err != nil {
			log.Fatalf("write http request: %v", err)
		}
		// Читаем ответ и печатаем в stdout.
		if _, err := io.Copy(os.Stdout, stream); err != nil {
			log.Fatalf("read http response: %v", err)
		}
		return
	}

	// По умолчанию просто прокидываем stdin/stdout через туннель.
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(stream, os.Stdin)
		_ = stream.Close()
	}()
	go func() {
		_, _ = io.Copy(os.Stdout, stream)
		close(done)
	}()

	select {
	case <-done:
	case <-ctx.Done():
	}
}

// clientConfigFromEnv возвращает конфиг для клиента (Port=0, KeyPath, BootstrapPeers из env). Не трогает flag.
func clientConfigFromEnv() *config.Config {
	cfg := &config.Config{
		Port:    0,
		KeyPath: "client.key",
	}
	if s := strings.TrimSpace(os.Getenv("BOOTSTRAP_PEERS")); s != "" {
		for _, p := range strings.Split(s, ",") {
			if t := strings.TrimSpace(p); t != "" {
				cfg.BootstrapPeers = append(cfg.BootstrapPeers, t)
			}
		}
	}
	return cfg
}

// runResolve поднимает ноду с DHT, разрешает имя и выводит multiaddr по одному на строку.
func runResolve(ctx context.Context, name string) error {
	cfg, _ := config.Load()
	if cfg == nil {
		cfg = &config.Config{}
	}
	cfg.Port = 0
	cfg.KeyPath = "client.key"
	cfg.EnableDHT = true
	cfg.DHTMode = "client"

	n, err := hypernode.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create node: %w", err)
	}
	defer n.Close()

	if n.DHT == nil {
		return fmt.Errorf("DHT not available")
	}
	ns := dns.NewNameSystem(n.DHT, n.Host)
	addrs, err := ns.Resolve(ctx, name)
	if err != nil {
		return fmt.Errorf("resolve %q: %w", name, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("resolve %q: no addresses", name)
	}
	for _, a := range addrs {
		fmt.Println(a.String())
	}
	return nil
}

