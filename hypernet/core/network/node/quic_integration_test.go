//go:build integration

package node

import (
	"context"
	"strings"
	"testing"
	"time"

	"hypernet-node/pkg/config"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// findAddrsBySubstring отбирает multiaddr по подстроке (например, "/udp/" или "/tcp/").
func findAddrsBySubstring(n *Node, substr string) []string {
	var out []string
	for _, a := range n.Host.Addrs() {
		s := a.String()
		if strings.Contains(s, substr) {
			out = append(out, s)
		}
	}
	return out
}

// TestNode_QuicAndTCPTransports проверяет, что:
//  1) нода, поднятая через Node.New, слушает как TCP, так и QUIC/UDP‑адреса;
//  2) клиент может подключиться к серверу, используя только QUIC‑адреса;
//  3) клиент может подключиться к серверу, используя только TCP‑адреса (fallback).
func TestNode_QuicAndTCPTransports(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	serverCfg := &config.Config{
		Port:      0,
		KeyPath:   "node-quic-server.key",
		EnableDHT: false,
	}
	server, err := New(ctx, serverCfg)
	if err != nil {
		t.Fatalf("New(server): %v", err)
	}
	defer server.Close()

	clientCfg := &config.Config{
		Port:      0,
		KeyPath:   "node-quic-client.key",
		EnableDHT: false,
	}
	client, err := New(ctx, clientCfg)
	if err != nil {
		t.Fatalf("New(client): %v", err)
	}
	defer client.Close()

	udpAddrs := findAddrsBySubstring(server, "/udp/")
	tcpAddrs := findAddrsBySubstring(server, "/tcp/")
	if len(udpAddrs) == 0 {
		t.Fatalf("server has no UDP/QUIC listen addresses")
	}
	if len(tcpAddrs) == 0 {
		t.Fatalf("server has no TCP listen addresses")
	}

	// Подключение по QUIC/UDP: используем только UDP‑адреса в AddrInfo.
	{
		var udpMulti []multiaddr.Multiaddr
		for _, s := range udpAddrs {
			m, err := multiaddr.NewMultiaddr(s)
			if err != nil {
				t.Fatalf("NewMultiaddr(udp %q): %v", s, err)
			}
			udpMulti = append(udpMulti, m)
		}
		info := peer.AddrInfo{
			ID:    server.Host.ID(),
			Addrs: udpMulti,
		}
		if err := client.Host.Connect(ctx, info); err != nil {
			t.Fatalf("client.Connect via QUIC/UDP addrs: %v", err)
		}
		hasQuic := false
		for _, c := range client.Host.Network().ConnsToPeer(server.Host.ID()) {
			if strings.Contains(c.RemoteMultiaddr().String(), "/udp/") {
				hasQuic = true
				break
			}
		}
		if !hasQuic {
			t.Fatalf("expected at least one QUIC/UDP connection to server")
		}
	}

	// Подключение по TCP: используем только TCP‑адреса в AddrInfo (fallback сценарий).
	{
		var tcpMulti []multiaddr.Multiaddr
		for _, s := range tcpAddrs {
			m, err := multiaddr.NewMultiaddr(s)
			if err != nil {
				t.Fatalf("NewMultiaddr(tcp %q): %v", s, err)
			}
			tcpMulti = append(tcpMulti, m)
		}
		info := peer.AddrInfo{
			ID:    server.Host.ID(),
			Addrs: tcpMulti,
		}
		if err := client.Host.Connect(ctx, info); err != nil {
			t.Fatalf("client.Connect via TCP addrs: %v", err)
		}
		hasTCP := false
		for _, c := range client.Host.Network().ConnsToPeer(server.Host.ID()) {
			addr := c.RemoteMultiaddr().String()
			if strings.Contains(addr, "/tcp/") && !strings.Contains(addr, "/udp/") {
				hasTCP = true
				break
			}
		}
		if !hasTCP {
			t.Fatalf("expected at least one pure TCP connection to server")
		}
	}
}

