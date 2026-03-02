package node

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"hypernet-node/hypernet/core/identity"
	"hypernet-node/pkg/config"
	"hypernet-node/hypernet/services/dns"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	dht "github.com/libp2p/go-libp2p-kad-dht"
	relayv2 "github.com/libp2p/go-libp2p/p2p/protocol/circuitv2/relay"
	"github.com/multiformats/go-multiaddr"

	"github.com/libp2p/go-libp2p/p2p/muxer/yamux"
	"github.com/libp2p/go-libp2p/p2p/security/noise"
)

// Node wraps a libp2p host and related services.
type Node struct {
	Host host.Host
	DHT  *dht.IpfsDHT
	// Protocols описывает список протоколов (адаптеров), которые поддерживает
	// данная нода на прикладном уровне (VLESS, Trojan, Shadowsocks, Hysteria2, plain и т.д.).
	// Поле используется как источник правды для публикации метаданных в DHT
	// и для клиентского выбора нод.
	Protocols []string
	// Region — логический регион/зона, в которой находится нода (например, "eu", "us-east-1").
	// Используется при публикации информации в DHT и выборе ближайших нод на клиенте.
	Region string
}

// New creates a new libp2p host configured according to cfg.
func New(ctx context.Context, cfg *config.Config) (*Node, error) {
	privKey, err := identity.LoadOrCreatePrivateKey(cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("load private key: %w", err)
	}

	tcpListen := fmt.Sprintf("/ip4/0.0.0.0/tcp/%d", cfg.Port)
	var listenAddrs []string
	listenAddrs = append(listenAddrs, tcpListen)
	// В обычном режиме включаем параллельно QUIC‑транспорт: слушаем тот же порт по UDP.
	// Это позволяет клиентам использовать как TCP, так и QUIC‑адреса одной и той же ноды.
	if !cfg.DisableQUIC {
		quicListen := fmt.Sprintf("/ip4/0.0.0.0/udp/%d/quic-v1", cfg.Port)
		listenAddrs = append(listenAddrs, quicListen)
	}

	var opts []libp2p.Option
	opts = append(opts,
		libp2p.ListenAddrStrings(listenAddrs...),
		libp2p.Identity(privKey),
		libp2p.Security(noise.ID, noise.New),
		libp2p.Muxer(yamux.ID, yamux.DefaultTransport),
	)

	// Enable circuit relay v2 service on dedicated relay nodes.
	if cfg.EnableRelay {
		var relayOpts []relayv2.Option

		res := relayv2.DefaultResources()
		if cfg.RelayMaxConnections > 0 {
			res.MaxCircuits = cfg.RelayMaxConnections
		}
		if cfg.RelayDataLimitBytes > 0 {
			if res.Limit == nil {
				res.Limit = relayv2.DefaultLimit()
			}
			res.Limit.Data = cfg.RelayDataLimitBytes
		}

		relayOpts = append(relayOpts, relayv2.WithResources(res))
		relayOpts = append(relayOpts, relayv2.WithMetricsTracer(relayv2.NewMetricsTracer()))

		opts = append(opts, libp2p.EnableRelayService(relayOpts...))
		// Provide AutoNAT service for other peers.
		opts = append(opts, libp2p.EnableNATService())
	}

	// AutoRelay using static relay candidates from bootstrap peers (if any).
	if len(cfg.BootstrapPeers) > 0 {
		var staticRelays []peer.AddrInfo
		for _, addrStr := range cfg.BootstrapPeers {
			maddr, err := multiaddr.NewMultiaddr(addrStr)
			if err != nil {
				continue
			}
			info, err := peer.AddrInfoFromP2pAddr(maddr)
			if err != nil {
				continue
			}
			staticRelays = append(staticRelays, *info)
		}
		if len(staticRelays) > 0 {
			opts = append(opts, libp2p.EnableAutoRelayWithStaticRelays(staticRelays))
		}
	}

	h, err := libp2p.New(opts...)
	if err != nil {
		return nil, fmt.Errorf("create libp2p host: %w", err)
	}

	node := &Node{
		Host:   h,
		Region: strings.TrimSpace(cfg.Region),
	}

	// Optional DHT (Kademlia).
	if cfg.EnableDHT {
		var dhtOpts []dht.Option

		switch strings.ToLower(cfg.DHTMode) {
		case "server":
			dhtOpts = append(dhtOpts, dht.Mode(dht.ModeServer))
		case "client":
			dhtOpts = append(dhtOpts, dht.Mode(dht.ModeClient))
		default:
			// ModeAuto by default.
		}

		// If custom bootstrap peers are provided, prefer them over libp2p defaults.
		if len(cfg.BootstrapPeers) > 0 {
			var infos []peer.AddrInfo
			for _, addrStr := range cfg.BootstrapPeers {
				maddr, err := multiaddr.NewMultiaddr(addrStr)
				if err != nil {
					continue
				}
				info, err := peer.AddrInfoFromP2pAddr(maddr)
				if err != nil {
					continue
				}
				infos = append(infos, *info)
			}
			if len(infos) > 0 {
				dhtOpts = append(dhtOpts, dht.BootstrapPeers(infos...))
			}
		}
		// DHT DNS: ключи /hypernet/name/<name>. Используем префикс /hypernet, чтобы зарегистрировать валидатор без конфликта с /pk и /ipns.
		dhtOpts = append(dhtOpts, dht.ProtocolPrefix("/hypernet"), dht.NamespacedValidator("hypernet", dns.DHTRecordValidator()))

		kad, err := dht.New(ctx, h, dhtOpts...)
		if err != nil {
			_ = h.Close()
			return nil, fmt.Errorf("create DHT: %w", err)
		}
		node.DHT = kad
	}

	// Connect to bootstrap peers if any are configured (retry so DHT routing table can be populated).
	if len(cfg.BootstrapPeers) > 0 {
		var infos []peer.AddrInfo
		for _, addrStr := range cfg.BootstrapPeers {
			maddr, err := multiaddr.NewMultiaddr(addrStr)
			if err != nil {
				continue
			}
			info, err := peer.AddrInfoFromP2pAddr(maddr)
			if err != nil {
				continue
			}
			infos = append(infos, *info)
		}
		for i, info := range infos {
			for attempt := 0; attempt < 5; attempt++ {
				if err := h.Connect(ctx, info); err != nil {
					log.Printf("bootstrap connect to %s (attempt %d): %v", info.ID, attempt+1, err)
					if attempt < 4 {
						time.Sleep(2 * time.Second)
					}
				} else {
					break
				}
			}
			if i < len(infos)-1 {
				time.Sleep(500 * time.Millisecond)
			}
		}
		// Brief pause so connections are fully established before Bootstrap() uses them.
		time.Sleep(1 * time.Second)
	}

	// DHT bootstrap: when bootstrap peers are set (e.g. DHT DNS registration), run synchronously
	// so the routing table is populated before DNS Register runs. Otherwise run in background.
	if node.DHT != nil {
		if len(cfg.BootstrapPeers) > 0 {
			_ = node.DHT.Bootstrap(ctx)
		} else {
			go func() {
				_ = node.DHT.Bootstrap(ctx)
			}()
		}
	}

	return node, nil
}

// Close shuts down the underlying libp2p host.
func (n *Node) Close() error {
	if n == nil || n.Host == nil {
		return nil
	}
	return n.Host.Close()
}

