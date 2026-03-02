package transport

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/multiformats/go-multiaddr"
)

const UnixTransportName = "unix"

type unixTransport struct{}

// Unix возвращает транспорт для Unix domain sockets (multiaddr вида /unix/path или /unix/run/socket.sock).
func Unix() Transport {
	return &unixTransport{}
}

func (t *unixTransport) Name() string {
	return UnixTransportName
}

func (t *unixTransport) Dial(ctx context.Context, addr multiaddr.Multiaddr) (net.Conn, error) {
	path, err := parseUnixMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{}
	return d.DialContext(ctx, "unix", path)
}

func (t *unixTransport) Listen(addr multiaddr.Multiaddr) (net.Listener, error) {
	path, err := parseUnixMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	return net.Listen("unix", path)
}

// parseUnixMultiaddr извлекает путь из multiaddr /unix/<path> (path может содержать слэши).
func parseUnixMultiaddr(ma multiaddr.Multiaddr) (string, error) {
	if ma == nil {
		return "", fmt.Errorf("multiaddr is nil")
	}
	s := ma.String()
	// Формат: /unix/run/socket.sock или /unix/tmp/foo
	if !strings.HasPrefix(s, "/unix/") {
		return "", fmt.Errorf("transport/unix: invalid multiaddr %q (need /unix/...)", s)
	}
	return s[len("/unix/"):], nil
}

func init() {
	DefaultRegistry.Register(Unix())
}

