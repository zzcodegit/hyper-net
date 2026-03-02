package transport

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/multiformats/go-multiaddr"
	kcp "github.com/xtaci/kcp-go/v5"
)

const KCPTransportName = "kcp"

type kcpTransport struct{}

// KCP returns mKCP transport (KCP over UDP).
func KCP() Transport { return &kcpTransport{} }

func (t *kcpTransport) Name() string { return KCPTransportName }

func (t *kcpTransport) Dial(ctx context.Context, addr multiaddr.Multiaddr) (net.Conn, error) {
	host, port, err := parseKCPMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	addrStr := net.JoinHostPort(host, port)
	// block nil = no encryption; 0,0 = no FEC
	conn, err := kcp.DialWithOptions(addrStr, nil, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("kcp dial: %w", err)
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	return conn, nil
}

func (t *kcpTransport) Listen(addr multiaddr.Multiaddr) (net.Listener, error) {
	host, port, err := parseKCPMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	addrStr := net.JoinHostPort(host, port)
	ln, err := kcp.ListenWithOptions(addrStr, nil, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("kcp listen: %w", err)
	}
	return ln, nil
}

// parseKCPMultiaddr extracts host and port from multiaddr with ip4/ip6/dns4 and udp (e.g. /ip4/127.0.0.1/udp/4000 or .../udp/4000/kcp).
func parseKCPMultiaddr(ma multiaddr.Multiaddr) (host, port string, err error) {
	if ma == nil {
		return "", "", fmt.Errorf("multiaddr is nil")
	}
	s := ma.String()
	parts := strings.Split(s, "/")
	host = ""
	port = ""
	for i := 1; i < len(parts); i++ {
		switch parts[i] {
		case "ip4", "ip6", "dns4", "dns6":
			if i+1 < len(parts) {
				host = parts[i+1]
				i++
			}
		case "udp":
			if i+1 < len(parts) {
				port = parts[i+1]
				i++
			}
		}
	}
	if host == "" || port == "" {
		return "", "", fmt.Errorf("transport/kcp: invalid multiaddr %q (need host and udp/port)", s)
	}
	return host, port, nil
}

func init() {
	DefaultRegistry.Register(KCP())
}

