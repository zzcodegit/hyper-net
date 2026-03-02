package transport

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/multiformats/go-multiaddr"
)

const TCPTransportName = "tcp"

type tcpTransport struct{}

// TCP возвращает транспорт для TCP (multiaddr вида /ip4/.../tcp/port или /ip6/.../tcp/port).
func TCP() Transport {
	return &tcpTransport{}
}

func (t *tcpTransport) Name() string {
	return TCPTransportName
}

func (t *tcpTransport) Dial(ctx context.Context, addr multiaddr.Multiaddr) (net.Conn, error) {
	network, host, port, err := parseTCPMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	addrStr := net.JoinHostPort(host, port)
	d := net.Dialer{}
	if deadline, ok := ctx.Deadline(); ok {
		d.Deadline = deadline
	} else {
		d.Deadline = time.Now().Add(30 * time.Second)
	}
	return d.DialContext(ctx, network, addrStr)
}

func (t *tcpTransport) Listen(addr multiaddr.Multiaddr) (net.Listener, error) {
	network, host, port, err := parseTCPMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	addrStr := net.JoinHostPort(host, port)
	return net.Listen(network, addrStr)
}

// parseTCPMultiaddr извлекает network ("tcp"), host и port из multiaddr с компонентами ip4/ip6 и tcp.
func parseTCPMultiaddr(ma multiaddr.Multiaddr) (network, host, port string, err error) {
	if ma == nil {
		return "", "", "", fmt.Errorf("multiaddr is nil")
	}
	s := ma.String()
	parts := strings.Split(s, "/")
	// ["", "ip4", "127.0.0.1", "tcp", "4001"] или ["", "ip6", "::1", "tcp", "4001"]
	host = ""
	port = ""
	for i := 1; i < len(parts); i++ {
		switch parts[i] {
		case "ip4", "ip6":
			if i+1 < len(parts) {
				host = parts[i+1]
				if parts[i] == "ip6" {
					network = "tcp6"
				} else {
					network = "tcp4"
				}
				i++
			}
		case "tcp":
			if i+1 < len(parts) {
				port = parts[i+1]
				i++
			}
		}
	}
	if host == "" || port == "" {
		return "", "", "", fmt.Errorf("transport/tcp: invalid multiaddr %q (need ip4/ip6 and tcp)", s)
	}
	if network == "" {
		network = "tcp"
	}
	return network, host, port, nil
}

func init() {
	DefaultRegistry.Register(TCP())
}

