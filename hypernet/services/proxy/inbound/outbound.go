package inbound

import (
	"context"
	"net"
)

// Outbound открывает исходящее соединение к целевому хосту. Используется
// inbound-прокси (SOCKS5, HTTP) для установки соединения к цели.
type Outbound interface {
	// Dial открывает TCP-соединение к host:port. network обычно "tcp".
	Dial(ctx context.Context, network, host string, port int) (net.Conn, error)
	// Name возвращает имя outbound (например "freedom", "blackhole").
	Name() string
}

