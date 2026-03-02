package daemon

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"hypernet-node/pkg/config"
	"hypernet-node/hypernet/services/dns"
	"hypernet-node/hypernet/services/proxy/inbound"
	metrics "hypernet-node/hypernet/services/proxy/metrics"
	"hypernet-node/pkg/protoadapters/hysteria2"
	"hypernet-node/pkg/protoadapters/plain"
	"hypernet-node/pkg/protoadapters/shadowsocks"
	"hypernet-node/pkg/protoadapters/trojan"
	"hypernet-node/pkg/protoadapters/vless"
	"hypernet-node/pkg/protoadapters/vmess"
	"hypernet-node/hypernet/services/proxy/protoiface"
	"hypernet-node/hypernet/services/proxy/protomanager"
	"hypernet-node/hypernet/services/proxy/protonegotiate"
	ping "hypernet-node/hypernet/core/network/ping"
	proxy "hypernet-node/hypernet/services/proxy/server"
	"hypernet-node/hypernet/services/vpn/reality"

	hypernode "hypernet-node/hypernet/core/network/node"

	"github.com/libp2p/go-libp2p/core/network"
	"github.com/multiformats/go-multiaddr"
	"github.com/oschwald/geoip2-golang"
)

// Run выполняет основной цикл ноды: создание хоста, регистрация протоколов, инбаунды, ожидание отмены контекста.
// Используется из cmd/node и из smoke-теста (с таймаутом контекста).
func Run(ctx context.Context, cfg *config.Config) error {
	n, err := hypernode.New(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create node: %w", err)
	}
	defer func() {
		if err := n.Close(); err != nil {
			log.Printf("error while closing node: %v", err)
		}
	}()

	// Register protocol handlers
	ping.Register(n.Host)

	// Инициализируем SQLite‑хранилище метрик (если путь задан).
	var metricsStore *metrics.Store
	var geoDB *geoip2.Reader
	if cfg.MetricsDBPath != "" {
		flush := cfg.MetricsFlushInterval
		if flush <= 0 {
			flush = time.Second
		}
		metricsPath := cfg.MetricsDBPath
		store, err := metrics.NewStore(metricsPath, flush)
		if err != nil && metricsPath == "./hypernet.db" {
			// В Docker/контейнерах текущая директория часто только для чтения — пробуем /tmp.
			metricsPath = "/tmp/hypernet.db"
			store, err = metrics.NewStore(metricsPath, flush)
		}
		if err != nil {
			log.Printf("metrics disabled (failed to init store at %q): %v", metricsPath, err)
		} else {
			metricsStore = store
			defer func() {
				if err := metricsStore.Close(); err != nil {
					log.Printf("metrics store close error: %v", err)
				}
			}()
		}
	}

	// Опциональная инициализация GeoIP2/GeoLite2 базы для client_region.
	if cfg.GeoIPDBPath != "" {
		if db, err := geoip2.Open(cfg.GeoIPDBPath); err != nil {
			log.Printf("geoip disabled (failed to open %q): %v", cfg.GeoIPDBPath, err)
		} else {
			geoDB = db
			defer func() {
				if err := geoDB.Close(); err != nil {
					log.Printf("geoip db close error: %v", err)
				}
			}()
		}
	}

	// Manager and negotiation handler for higher-level protocols (plain, VLESS, Trojan, Shadowsocks, Hysteria2, etc.).
	// Список и порядок протоколов задаётся через PROXY_PROTOCOLS (или -proxy-protocols); пусто = все по умолчанию.
	defaultProtoOrder := []string{
		vless.ProtocolName,
		vmess.ProtocolName,
		trojan.ProtocolName,
		shadowsocks.ProtocolName,
		hysteria2.ProtocolName,
		plain.ProtocolName,
	}
	preferredProtos := defaultProtoOrder
	if len(cfg.ProxyProtocols) > 0 {
		allowed := make(map[string]bool)
		for _, n := range defaultProtoOrder {
			allowed[n] = true
		}
		preferredProtos = nil
		for _, n := range cfg.ProxyProtocols {
			if allowed[n] {
				preferredProtos = append(preferredProtos, n)
			}
		}
		if len(preferredProtos) == 0 {
			preferredProtos = defaultProtoOrder
		}
	}
	protoMgr := protomanager.NewManager(preferredProtos)
	registerProtocol := func(name string, p protoiface.Protocol) {
		for _, n := range preferredProtos {
			if n == name {
				protoMgr.Register(p)
				return
			}
		}
	}
	registerProtocol(vless.ProtocolName, &vless.VLESSProtocol{})
	registerProtocol(vmess.ProtocolName, &vmess.VMESSProtocol{})
	registerProtocol(trojan.ProtocolName, &trojan.TrojanProtocol{})
	registerProtocol(shadowsocks.ProtocolName, &shadowsocks.ShadowsocksProtocol{})
	registerProtocol(hysteria2.ProtocolName, &hysteria2.Hysteria2Protocol{})
	registerProtocol(plain.ProtocolName, &plain.PlainProtocol{})

	// Сохраняем список поддерживаемых протоколов на ноде — это пригодится
	// для публикации метаданных в DHT и отбора нод на клиенте.
	n.Protocols = protoMgr.SupportedNames()

	// Протокол VLESS использует общий токен аутентификации, который пока
	// задаётся через переменную окружения и передаётся в пакет proxy.
	proxyServer := proxy.NewServer()
	if metricsStore != nil {
		proxyServer.SetMetricsStore(metricsStore)
		proxyServer.SetMetricsQualityInterval(cfg.MetricsQualityInterval)
	}
	// Передаём регион ноды и GeoIP‑ридер для client_region.
	proxyServer.SetNodeRegion(n.Region)
	if geoDB != nil {
		proxyServer.SetGeoIPReader(geoDB)
	}
	if token := os.Getenv("HYPERNET_VLESS_TOKEN"); token != "" {
		proxyServer.SetVLESSAuthToken(token)
	}
	// Обфускация на сервере (паддинг и задержка записи) — параметры должны совпадать с клиентом.
	pMin, pMax := 0, 0
	if v := os.Getenv("HYPERNET_OBFS_PADDING_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			pMin = n
			if v2 := os.Getenv("HYPERNET_OBFS_PADDING_MAX"); v2 != "" {
				if m, err := strconv.Atoi(v2); err == nil && m >= pMin {
					pMax = m
				}
			}
		}
	}
	dMin, dMax := time.Duration(0), time.Duration(0)
	if v := os.Getenv("HYPERNET_OBFS_WRITE_DELAY_MIN"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			dMin = d
			dMax = d
			if v2 := os.Getenv("HYPERNET_OBFS_WRITE_DELAY_MAX"); v2 != "" {
				if d2, err := time.ParseDuration(v2); err == nil && d2 >= dMin {
					dMax = d2
				}
			}
		}
	}
	if pMax > 0 || dMax > 0 {
		proxyServer.SetObfuscation(pMin, pMax, dMin, dMax)
	}
	proxyServer.Register(n.Host)

	// Если на ноде включён DHT, периодически объявляем в нём поддержку всех
	// протоколов из n.Protocols. Это позволяет клиентам находить ноды,
	// поддерживающие конкретные адаптеры.
	if n.DHT != nil && len(n.Protocols) > 0 {
		go func() {
			// Первичное объявление сразу после старта.
			if err := n.AnnounceProtocols(ctx, true); err != nil {
				log.Printf("announce protocols in DHT failed: %v", err)
			}
			ticker := time.NewTicker(10 * time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := n.AnnounceProtocols(ctx, true); err != nil {
						log.Printf("periodic announce protocols in DHT failed: %v", err)
					}
				}
			}
		}()
	}

	// DHT DNS: регистрация человекочитаемых имён в DHT и периодическое обновление.
	// Первая регистрация повторяется с backoff, т.к. DHT routing table может быть ещё пуста при старте.
	if n.DHT != nil && len(cfg.DNSNames) > 0 && cfg.DNSTTL > 0 {
		var addrs []multiaddr.Multiaddr
		for _, a := range n.Host.Addrs() {
			full, err := multiaddr.NewMultiaddr(a.String() + "/p2p/" + n.Host.ID().String())
			if err != nil {
				continue
			}
			addrs = append(addrs, full)
		}
		if len(addrs) > 0 {
			ns := dns.NewNameSystem(n.DHT, n.Host)
			names := make([]string, len(cfg.DNSNames))
			copy(names, cfg.DNSNames)
			go func() {
				const initialDelay = 5 * time.Second  // даём DHT время на bootstrap и заполнение routing table
				const backoff = 3 * time.Second
				const maxAttempts = 25
				select {
				case <-ctx.Done():
					return
				case <-time.After(initialDelay):
				}
				for attempt := 0; attempt < maxAttempts; attempt++ {
					select {
					case <-ctx.Done():
						return
					default:
					}
					allOk := true
					for _, name := range names {
						if err := ns.Register(ctx, name, addrs, cfg.DNSTTL); err != nil {
							if strings.Contains(err.Error(), "already registered by another peer") {
								log.Printf("dns: warning: name %q already registered by another peer, skipping", name)
							} else {
								log.Printf("dns register %q (attempt %d): %v", name, attempt+1, err)
								allOk = false
							}
						} else {
							log.Printf("dns: registered name %q", name)
						}
					}
					if allOk {
						break
					}
					if attempt < maxAttempts-1 {
						select {
						case <-ctx.Done():
							return
						case <-time.After(backoff):
						}
					}
				}
				ticker := time.NewTicker(cfg.DNSRefreshInterval)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						for _, name := range names {
							if err := ns.Refresh(ctx, name); err != nil {
								log.Printf("dns refresh %q: %v", name, err)
							}
						}
					}
				}
			}()
		}
	}

	n.Host.SetStreamHandler(protonegotiate.ProtocolID, func(s network.Stream) {
		// Используем общий контекст ноды; в случае ошибки просто логируем.
		chosen, err := protonegotiate.HandleStream(ctx, s, protoMgr)
		if err != nil {
			log.Printf("protocol negotiation error: %v", err)
			return
		}
		proxyServer.RememberChosenProtocol(s.Conn().RemotePeer(), chosen)
	})

	// Inbound: SOCKS5 и HTTP CONNECT прокси (outbound = freedom или blackhole).
	var outbound inbound.Outbound
	switch cfg.Outbound {
	case "blackhole":
		outbound = inbound.NewBlackhole()
	default:
		outbound = inbound.NewFreedom(0)
	}
	if cfg.Socks5Listen != "" {
		socks5 := inbound.NewServerSOCKS5(outbound)
		go func() {
			if err := socks5.ListenAndServe(ctx, cfg.Socks5Listen); err != nil && ctx.Err() == nil {
				log.Printf("socks5 server: %v", err)
			}
		}()
	}
	if cfg.HTTPProxyListen != "" {
		httpProxy := inbound.NewServerHTTPProxy(outbound)
		go func() {
			if err := httpProxy.ListenAndServe(ctx, cfg.HTTPProxyListen); err != nil && ctx.Err() == nil {
				log.Printf("http proxy server: %v", err)
			}
		}()
	}
	if cfg.DokodemoListen != "" && cfg.DokodemoTarget != "" {
		redirect, err := inbound.NewRedirect(cfg.DokodemoTarget)
		if err != nil {
			log.Printf("dokodemo-door disabled: invalid target %q: %v", cfg.DokodemoTarget, err)
		} else {
			dokodemo := inbound.NewDokodemoDoor(redirect)
			go func() {
				if err := dokodemo.ListenAndServe(ctx, cfg.DokodemoListen); err != nil && ctx.Err() == nil {
					log.Printf("dokodemo-door: %v", err)
				}
			}()
		}
	}

	// REALITY: маскировка под реальный HTTPS (совместимо с Xray). Полная интеграция с libp2p — в планах.
	if cfg.RealityListen != "" && cfg.RealityDest != "" && cfg.RealityPrivateKey != "" {
		realityCfg := &reality.ServerConfig{
			Dest:        cfg.RealityDest,
			PrivateKey:  cfg.RealityPrivateKey,
			ServerNames: cfg.RealityServerNames,
			ShortIds:    cfg.RealityShortIds,
		}
		ln, err := reality.ListenREALITY(cfg.RealityListen, realityCfg)
		if err != nil {
			log.Printf("REALITY disabled: %v", err)
		} else {
			log.Printf("REALITY listening on %s (masquerading as %s)", cfg.RealityListen, cfg.RealityDest)
			go func() {
				defer ln.Close()
				go func() { <-ctx.Done(); ln.Close() }()
				for {
					conn, err := ln.Accept()
					if err != nil {
						if ctx.Err() != nil {
							return
						}
						log.Printf("REALITY accept: %v", err)
						return
					}
					// Пока только принимаем, логируем и закрываем; передача в libp2p — отдельная задача.
					go func(c net.Conn) {
						defer c.Close()
						log.Printf("REALITY connection from %s (handshake OK)", c.RemoteAddr())
					}(conn)
				}
			}()
		}
	}

	// Print basic info
	fmt.Printf("Peer ID: %s\n", n.Host.ID().String())
	fmt.Println("Listen addresses:")
	for _, addr := range n.Host.Addrs() {
		full := fmt.Sprintf("%s/p2p/%s", addr.String(), n.Host.ID().String())
		fmt.Printf("  %s\n", full)
	}

	// If target is specified, act as a ping client and exit
	if cfg.Target != "" {
		if err := ping.PingTarget(ctx, n.Host, cfg.Target, cfg.PingCount); err != nil {
			return fmt.Errorf("ping: %w", err)
		}
		return nil
	}

	// Otherwise, keep running until context is cancelled
	<-ctx.Done()
	log.Println("node stopped")
	return nil
}

