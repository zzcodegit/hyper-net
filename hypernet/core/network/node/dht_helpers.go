package node

import (
	"context"
	"fmt"
	"strings"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multihash"
)

// ProvidersForKey uses the DHT (if enabled) to find providers for the given string key.
// The key is converted into a CID using a deterministic multihash of the UTF-8 bytes.
func (n *Node) ProvidersForKey(ctx context.Context, key string, max int) ([]peer.AddrInfo, error) {
	if n == nil || n.DHT == nil {
		return nil, fmt.Errorf("DHT is not enabled on this node")
	}
	if max <= 0 {
		max = 1
	}

	c, err := stringKeyToCID(key)
	if err != nil {
		return nil, err
	}

	outCh := n.DHT.FindProvidersAsync(ctx, c, max)
	var providers []peer.AddrInfo
	for info := range outCh {
		providers = append(providers, info)
	}
	return providers, nil
}

// protocolKey формирует детерминированный ключ для публикации поддержки
// конкретного протокола в DHT.
func protocolKey(protoName string) string {
	return "hypernet:proto:" + protoName
}

// protocolRegionKey формирует ключ для публикации поддержки протокола с учётом региона.
// Это позволяет клиентам искать ноды не только по протоколу, но и по региону.
func protocolRegionKey(protoName, region string) string {
	return "hypernet:proto:" + protoName + ":region:" + region
}

// AnnounceProtocols объявляет текущую ноду провайдером для всех протоколов
// из n.Protocols. Используется для публикации метаданных в DHT.
func (n *Node) AnnounceProtocols(ctx context.Context, announce bool) error {
	if n == nil || n.DHT == nil {
		return fmt.Errorf("DHT is not enabled on this node")
	}
	if len(n.Protocols) == 0 {
		return nil
	}
	for _, name := range n.Protocols {
		if name == "" {
			continue
		}
		if err := n.ProvideKey(ctx, protocolKey(name), announce); err != nil {
			return fmt.Errorf("announce protocol %q: %w", name, err)
		}
		if n.Region != "" {
			if err := n.ProvideKey(ctx, protocolRegionKey(name, n.Region), announce); err != nil {
				return fmt.Errorf("announce protocol %q region %q: %w", name, n.Region, err)
			}
		}
	}
	return nil
}

// FindProtocolProviders ищет провайдеров, поддерживающих указанный протокол
// (адаптер), используя DHT.
func (n *Node) FindProtocolProviders(ctx context.Context, protoName string, max int) ([]peer.AddrInfo, error) {
	return n.ProvidersForKey(ctx, protocolKey(protoName), max)
}

// FindRegionProtocolProviders ищет провайдеров, поддерживающих указанный протокол
// в заданном регионе (если такие записи были объявлены через AnnounceProtocols).
func (n *Node) FindRegionProtocolProviders(ctx context.Context, protoName, region string, max int) ([]peer.AddrInfo, error) {
	if strings.TrimSpace(region) == "" {
		return n.FindProtocolProviders(ctx, protoName, max)
	}
	return n.ProvidersForKey(ctx, protocolRegionKey(protoName, region), max)
}

// ProvideKey announces this node as a provider for the given string key in the DHT.
func (n *Node) ProvideKey(ctx context.Context, key string, announce bool) error {
	if n == nil || n.DHT == nil {
		return fmt.Errorf("DHT is not enabled on this node")
	}
	c, err := stringKeyToCID(key)
	if err != nil {
		return err
	}
	if err := n.DHT.Provide(ctx, c, announce); err != nil {
		// В небольших или только что запущенных сетях DHT может ещё не иметь
		// пиров в таблице маршрутизации и возвращать ошибку вида
		// "failed to find any peer in table". Для локальных/интеграционных
		// сценариев это не считается фатальной ситуацией — локальный узел
		// всё равно знает о собственной провайдерской записи — поэтому
		// подавляем именно эту ошибку.
		if strings.Contains(err.Error(), "failed to find any peer in table") {
			return nil
		}
		return err
	}
	return nil
}

// FindPeer looks up a peer by ID via the DHT (if enabled).
// If the DHT returns "failed to find any peer in table" but we are already connected
// to the peer (e.g. after explicit Connect in tests), returns Peerstore info as fallback.
func (n *Node) FindPeer(ctx context.Context, id peer.ID) (peer.AddrInfo, error) {
	if n == nil || n.DHT == nil {
		return peer.AddrInfo{}, fmt.Errorf("DHT is not enabled on this node")
	}
	info, err := n.DHT.FindPeer(ctx, id)
	if err != nil {
		if strings.Contains(err.Error(), "failed to find any peer in table") && n.Host != nil {
			if n.Host.Network().Connectedness(id) == network.Connected {
				fallback := n.Host.Peerstore().PeerInfo(id)
				if len(fallback.Addrs) > 0 {
					return fallback, nil
				}
			}
		}
		return peer.AddrInfo{}, err
	}
	return info, nil
}

// stringKeyToCID deterministically maps an arbitrary string key to a CID.
func stringKeyToCID(key string) (cid.Cid, error) {
	if key == "" {
		return cid.Cid{}, fmt.Errorf("empty key")
	}
	// Use a standard multihash function (SHA2-256) over the UTF-8 bytes.
	sum, err := multihash.Sum([]byte(key), multihash.SHA2_256, -1)
	if err != nil {
		return cid.Cid{}, err
	}
	return cid.NewCidV1(cid.Raw, sum), nil
}

