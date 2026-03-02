package transport

import (
	"context"
	"net"

	"github.com/multiformats/go-multiaddr"
)

// Transport описывает способ установки соединения и прослушивания (TCP, WebSocket, gRPC, mKCP и т.д.).
type Transport interface {
	// Dial устанавливает соединение с удалённой нодой через данный транспорт.
	Dial(ctx context.Context, addr multiaddr.Multiaddr) (conn net.Conn, err error)
	// Listen начинает слушать на указанном адресе и возвращает listener.
	Listen(addr multiaddr.Multiaddr) (net.Listener, error)
	// Name возвращает строковый идентификатор транспорта (например, "tcp", "ws", "grpc", "kcp").
	Name() string
}

// Registry хранит транспорты по имени.
type Registry struct {
	byName map[string]Transport
}

// NewRegistry создаёт пустой реестр транспортов.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Transport)}
}

// Register добавляет транспорт в реестр.
func (r *Registry) Register(t Transport) {
	if t == nil {
		return
	}
	if r.byName == nil {
		r.byName = make(map[string]Transport)
	}
	r.byName[t.Name()] = t
}

// Get возвращает транспорт по имени.
func (r *Registry) Get(name string) (Transport, bool) {
	t, ok := r.byName[name]
	return t, ok
}

// ByMultiaddr возвращает транспорт по первому транспортному протоколу в multiaddr.
func (r *Registry) ByMultiaddr(ma multiaddr.Multiaddr) (Transport, bool) {
	name := multiaddrTransportName(ma)
	if name == "" {
		return nil, false
	}
	return r.Get(name)
}

// DefaultRegistry — глобальный реестр транспортов. TCP регистрируется в init пакета tcp.
var DefaultRegistry = NewRegistry()

func multiaddrTransportName(ma multiaddr.Multiaddr) string {
	if ma == nil {
		return ""
	}
	for _, p := range ma.Protocols() {
		switch p.Name {
		case "tcp", "udp", "ws", "wss", "grpc", "kcp", "unix":
			return p.Name
		}
	}
	return ""
}

