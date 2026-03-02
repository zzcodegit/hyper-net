package routing

import (
	"context"

	netnode "hypernet-node/hypernet/core/network/node"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// ProviderDiscovery описывает интерфейс для поиска провайдеров протоколов и fallback
// по уже подключённым пирам.
type ProviderDiscovery interface {
	FindProtocolProviders(ctx context.Context, protoName string, max int) ([]peer.AddrInfo, error)
	FindRegionProtocolProviders(ctx context.Context, protoName, region string, max int) ([]peer.AddrInfo, error)

	ConnectedPeers() []peer.ID
	PeerAddrs(peer.ID) []multiaddr.Multiaddr

	Host() host.Host
}

// NodeProviderDiscovery — адаптер поверх core/network/node.Node, реализующий ProviderDiscovery.
type NodeProviderDiscovery struct {
	Node *netnode.Node
}

func NewNodeProviderDiscovery(n *netnode.Node) *NodeProviderDiscovery {
	return &NodeProviderDiscovery{Node: n}
}

func (d *NodeProviderDiscovery) FindProtocolProviders(ctx context.Context, protoName string, max int) ([]peer.AddrInfo, error) {
	return d.Node.FindProtocolProviders(ctx, protoName, max)
}

func (d *NodeProviderDiscovery) FindRegionProtocolProviders(ctx context.Context, protoName, region string, max int) ([]peer.AddrInfo, error) {
	return d.Node.FindRegionProtocolProviders(ctx, protoName, region, max)
}

func (d *NodeProviderDiscovery) ConnectedPeers() []peer.ID {
	if d.Node == nil || d.Node.Host == nil {
		return nil
	}
	return d.Node.Host.Network().Peers()
}

func (d *NodeProviderDiscovery) PeerAddrs(id peer.ID) []multiaddr.Multiaddr {
	if d.Node == nil || d.Node.Host == nil {
		return nil
	}
	return d.Node.Host.Peerstore().Addrs(id)
}

func (d *NodeProviderDiscovery) Host() host.Host {
	if d.Node == nil {
		return nil
	}
	return d.Node.Host
}

