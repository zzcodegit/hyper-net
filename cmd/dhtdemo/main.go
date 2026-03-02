package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"hypernet-node/pkg/config"
	hypernode "hypernet-node/hypernet/core/network/node"

	"github.com/libp2p/go-libp2p/core/peer"
)

type demoNode struct {
	name string
	n    *hypernode.Node
}

func mustNewNode(ctx context.Context, name string, cfg *config.Config) *demoNode {
	n, err := hypernode.New(ctx, cfg)
	if err != nil {
		log.Fatalf("create node %s: %v", name, err)
	}
	return &demoNode{name: name, n: n}
}

func (d *demoNode) Close() {
	if d == nil || d.n == nil {
		return
	}
	if err := d.n.Close(); err != nil {
		log.Printf("close node %s: %v", d.name, err)
	}
}

func connect(ctx context.Context, from, to *hypernode.Node, nameFrom, nameTo string) {
	info := peer.AddrInfo{
		ID:    to.Host.ID(),
		Addrs: to.Host.Addrs(),
	}
	if err := from.Host.Connect(ctx, info); err != nil {
		log.Fatalf("connect %s -> %s: %v", nameFrom, nameTo, err)
	}
}

// localBootstrapAddr returns a multiaddr for the node suitable for bootstrap from the same host:
// uses 127.0.0.1 so that other nodes can dial even when the node listens on 0.0.0.0.
func localBootstrapAddr(n *hypernode.Node) string {
	addrs := n.Host.Addrs()
	if len(addrs) == 0 {
		return ""
	}
	s := addrs[0].String()
	s = strings.Replace(s, "/ip4/0.0.0.0/", "/ip4/127.0.0.1/", 1)
	return fmt.Sprintf("%s/p2p/%s", s, n.Host.ID().String())
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()

	log.Println("DHT demo: starting nodes A, B, C (servers) and D (client)...")

	// Node A: full DHT server.
	aCfg := &config.Config{
		Port:      0,
		KeyPath:   "dhtdemo-a.key",
		EnableDHT: true,
		DHTMode:   "server",
	}
	nodeA := mustNewNode(ctx, "A", aCfg)
	defer nodeA.Close()

	if len(nodeA.n.Host.Addrs()) == 0 {
		log.Fatalf("node A has no listen addrs")
	}
	bootstrapA := localBootstrapAddr(nodeA.n)
	if bootstrapA == "" {
		log.Fatalf("node A: could not build bootstrap addr")
	}

	// Nodes B and C: full DHT servers that bootstrap from A.
	bCfg := &config.Config{
		Port:           0,
		KeyPath:        "dhtdemo-b.key",
		EnableDHT:      true,
		DHTMode:        "server",
		BootstrapPeers: []string{bootstrapA},
	}
	cCfg := &config.Config{
		Port:           0,
		KeyPath:        "dhtdemo-c.key",
		EnableDHT:      true,
		DHTMode:        "server",
		BootstrapPeers: []string{bootstrapA},
	}
	nodeB := mustNewNode(ctx, "B", bCfg)
	defer nodeB.Close()
	nodeC := mustNewNode(ctx, "C", cCfg)
	defer nodeC.Close()

	// Node D: DHT client bootstrapping from A, B, C — больше пиров в таблице маршрутизации.
	bootstrapB := localBootstrapAddr(nodeB.n)
	bootstrapC := localBootstrapAddr(nodeC.n)
	bootstrapPeersD := []string{bootstrapA}
	if bootstrapB != "" {
		bootstrapPeersD = append(bootstrapPeersD, bootstrapB)
	}
	if bootstrapC != "" {
		bootstrapPeersD = append(bootstrapPeersD, bootstrapC)
	}
	dCfg := &config.Config{
		Port:           0,
		KeyPath:        "dhtdemo-d.key",
		EnableDHT:      true,
		DHTMode:        "client",
		BootstrapPeers: bootstrapPeersD,
	}
	nodeD := mustNewNode(ctx, "D", dCfg)
	defer nodeD.Close()

	// Явно соединяем узлы, чтобы гарантировать единый overlay и заполненные DHT-таблицы.
	connect(ctx, nodeB.n, nodeA.n, "B", "A")
	connect(ctx, nodeC.n, nodeA.n, "C", "A")
	connect(ctx, nodeB.n, nodeC.n, "B", "C") // B↔C для надёжного Provide/FindProviders
	connect(ctx, nodeD.n, nodeA.n, "D", "A")

	log.Println("DHT demo: waiting for DHT convergence...")
	time.Sleep(8 * time.Second)

	if err := testFindPeer(ctx, nodeD.n, nodeC.n.Host.ID()); err != nil {
		log.Fatalf("FindPeer test failed: %v", err)
	}

	if err := testProvideAndFindProviders(ctx, nodeA.n, nodeC.n, nodeD.n); err != nil {
		log.Fatalf("Provide/FindProviders test failed: %v", err)
	}

	if err := testRoutingPeers(nodeD.n); err != nil {
		log.Fatalf("routing peers test failed: %v", err)
	}

	log.Println("DHT demo: all tests passed.")
}

func testFindPeer(ctx context.Context, client *hypernode.Node, targetID peer.ID) error {
	log.Println("DHT demo: testing FindPeer from D (client) to C (server)...")

	info, err := client.FindPeer(ctx, targetID)
	if err != nil {
		return fmt.Errorf("FindPeer: %w", err)
	}

	log.Printf("DHT demo: FindPeer found peer %s at %d addrs", info.ID, len(info.Addrs))
	if len(info.Addrs) == 0 {
		return fmt.Errorf("FindPeer returned zero addresses for %s", info.ID)
	}
	return nil
}

func testProvideAndFindProviders(ctx context.Context, nodeA, nodeC, client *hypernode.Node) error {
	log.Println("DHT demo: testing Provide/FindProviders for keys \"my-file\" and \"test-key-123\"...")

	keys := []string{"my-file", "test-key-123"}
	for _, key := range keys {
		if err := nodeA.ProvideKey(ctx, key, true); err != nil {
			return fmt.Errorf("ProvideKey on A for %q: %w", key, err)
		}
		time.Sleep(1 * time.Second) // даём первому provide время разойтись по DHT
		if err := nodeC.ProvideKey(ctx, key, true); err != nil {
			return fmt.Errorf("ProvideKey on C for %q: %w", key, err)
		}

		time.Sleep(5 * time.Second) // время на репликацию и ответы FindProviders

		providers, err := client.ProvidersForKey(ctx, key, 10)
		if err != nil {
			return fmt.Errorf("ProvidersForKey for %q: %w", key, err)
		}

		log.Printf("DHT demo: Found %d providers for key %q", len(providers), key)
		if len(providers) < 1 {
			return fmt.Errorf("expected at least 1 provider for key %q, got %d", key, len(providers))
		}

		foundA := false
		foundC := false
		for _, info := range providers {
			log.Printf("  provider: %s", info.ID)
			if info.ID == nodeA.Host.ID() {
				foundA = true
			}
			if info.ID == nodeC.Host.ID() {
				foundC = true
			}
		}
		// В малой DHT репликация одного ключа на двух провайдеров может дать только одного в ответе.
		if !foundA && !foundC {
			return fmt.Errorf("expected at least one of A (%s) or C (%s) as provider for key %q, got %d provider(s)",
				nodeA.Host.ID(), nodeC.Host.ID(), key, len(providers))
		}
	}
	return nil
}

func testRoutingPeers(n *hypernode.Node) error {
	log.Println("DHT demo: testing routing/network peers on node D...")
	peers := n.Host.Network().Peers()
	log.Printf("DHT demo: Known peers in routing/network: %d", len(peers))
	if len(peers) == 0 {
		return fmt.Errorf("expected to know at least one peer in DHT/network")
	}
	for _, p := range peers {
		log.Printf("  peer: %s", p.String())
	}
	return nil
}

