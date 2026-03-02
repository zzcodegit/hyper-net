package protonegotiate

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"
	"hypernet-node/hypernet/services/proxy/protomanager"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// testProtocol — минимальная реализация protoiface.Protocol для тестов.
type testProtocol struct {
	name string
}

func (t *testProtocol) Name() string { return t.name }

func (t *testProtocol) Handshake(stream io.ReadWriteCloser, cfg *protoiface.Config) (protoiface.Session, error) {
	return stream, nil
}

func (t *testProtocol) Wrap(conn net.Conn, cfg *protoiface.Config) (io.ReadWriteCloser, error) {
	return conn, nil
}

func (t *testProtocol) Features() protoiface.Features {
	return protoiface.Features{Transports: []string{"tcp"}}
}

// helper для создания простого libp2p-хоста.
func newTestHost(t *testing.T) host.Host {
	t.Helper()
	h, err := libp2p.New()
	if err != nil {
		t.Fatalf("libp2p.New: %v", err)
	}
	return h
}

func connectHosts(t *testing.T, a, b host.Host) {
	t.Helper()
	if len(b.Addrs()) == 0 {
		t.Fatalf("host b has no addrs")
	}
	maddr := b.Addrs()[0]
	full, err := multiaddr.NewMultiaddr(maddr.String() + "/p2p/" + b.ID().String())
	if err != nil {
		t.Fatalf("multiaddr: %v", err)
	}
	info, err := peer.AddrInfoFromP2pAddr(full)
	if err != nil {
		t.Fatalf("AddrInfoFromP2pAddr: %v", err)
	}
	if err := a.Connect(context.Background(), *info); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

func TestNegotiation_Success(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverHost := newTestHost(t)
	defer serverHost.Close()
	clientHost := newTestHost(t)
	defer clientHost.Close()

	// Менеджер протоколов на сервере и клиенте.
	serverMgr := protomanager.NewManager([]string{"vless", "trojan"})
	clientMgr := protomanager.NewManager([]string{"vless", "trojan"})

	serverMgr.Register(&testProtocol{name: "vless"})
	serverMgr.Register(&testProtocol{name: "trojan"})

	clientMgr.Register(&testProtocol{name: "vless"})
	clientMgr.Register(&testProtocol{name: "trojan"})

	// Регистрация обработчика negotiation-протокола на сервере.
	serverHost.SetStreamHandler(ProtocolID, func(s network.Stream) {
		_, _ = HandleStream(ctx, s, serverMgr)
	})

	connectHosts(t, clientHost, serverHost)

	chosen, _, err := ClientNegotiate(ctx, clientHost, serverHost.ID(), clientMgr)
	if err != nil {
		t.Fatalf("ClientNegotiate failed: %v", err)
	}
	if chosen == "" {
		t.Fatalf("expected chosen protocol, got empty string")
	}
}

func TestNegotiation_NoCommon(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	serverHost := newTestHost(t)
	defer serverHost.Close()
	clientHost := newTestHost(t)
	defer clientHost.Close()

	serverMgr := protomanager.NewManager(nil)
	clientMgr := protomanager.NewManager(nil)

	serverMgr.Register(&testProtocol{name: "vless"})
	clientMgr.Register(&testProtocol{name: "hysteria2"})

	serverHost.SetStreamHandler(ProtocolID, func(s network.Stream) {
		_, _ = HandleStream(ctx, s, serverMgr)
	})

	connectHosts(t, clientHost, serverHost)

	_, _, err := ClientNegotiate(ctx, clientHost, serverHost.ID(), clientMgr)
	if err == nil {
		t.Fatalf("expected error when there is no common protocol")
	}
	if msg := err.Error(); !strings.Contains(msg, "no common protocol") {
		t.Fatalf("unexpected error message: %q", msg)
	}
}

// TestNegotiation_ClientPriority_Table проверяет, что:
//   - выбор общего протокола учитывает порядок предпочтений клиента;
//   - различные комбинации списков протоколов на клиенте и сервере
//     дают ожидаемый результат (включая отсутствие пересечения).
func TestNegotiation_ClientPriority_Table(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		serverProtocols []string
		clientProtocols []string
		wantChosen      string
		wantErr         bool
	}{
		{
			name:            "client_prefers_trojan_over_vless",
			serverProtocols: []string{"vless", "trojan"},
			clientProtocols: []string{"trojan", "vless"},
			wantChosen:      "trojan",
			wantErr:         false,
		},
		{
			name:            "client_prefers_vless_when_both_support",
			serverProtocols: []string{"vless", "trojan"},
			clientProtocols: []string{"vless", "trojan"},
			wantChosen:      "vless",
			wantErr:         false,
		},
		{
			name:            "single_common_protocol",
			serverProtocols: []string{"trojan"},
			clientProtocols: []string{"vless", "trojan"},
			wantChosen:      "trojan",
			wantErr:         false,
		},
		{
			name:            "no_common_protocols",
			serverProtocols: []string{"vless"},
			clientProtocols: []string{"hysteria2"},
			wantChosen:      "",
			wantErr:         true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			serverHost := newTestHost(t)
			defer serverHost.Close()
			clientHost := newTestHost(t)
			defer clientHost.Close()

			// Используем preferred-списки, чтобы SupportedNames возвращал
			// протоколы в нужном порядке (особенно на клиенте).
			serverMgr := protomanager.NewManager(tc.serverProtocols)
			clientMgr := protomanager.NewManager(tc.clientProtocols)

			for _, name := range tc.serverProtocols {
				serverMgr.Register(&testProtocol{name: name})
			}
			for _, name := range tc.clientProtocols {
				clientMgr.Register(&testProtocol{name: name})
			}

			serverHost.SetStreamHandler(ProtocolID, func(s network.Stream) {
				_, _ = HandleStream(ctx, s, serverMgr)
			})

			connectHosts(t, clientHost, serverHost)

			chosen, _, err := ClientNegotiate(ctx, clientHost, serverHost.ID(), clientMgr)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (chosen=%q)", chosen)
				}
				return
			}

			if err != nil {
				t.Fatalf("ClientNegotiate error: %v", err)
			}
			if chosen != tc.wantChosen {
				t.Fatalf("unexpected chosen protocol: got %q, want %q", chosen, tc.wantChosen)
			}
		})
	}
}

