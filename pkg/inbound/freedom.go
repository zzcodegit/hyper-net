package inbound

import (
	"context"
	"net"
	"strconv"
	"time"
)

const FreedomOutboundName = "freedom"

// Freedom — исходящий «напрямую»: обычный net.Dial к цели (как Xray Freedom).
type Freedom struct {
	Dialer *net.Dialer
}

// NewFreedom создаёт outbound Freedom с опциональным таймаутом.
func NewFreedom(timeout time.Duration) *Freedom {
	d := &net.Dialer{}
	if timeout > 0 {
		d.Timeout = timeout
	}
	return &Freedom{Dialer: d}
}


// Dial устанавливает TCP-соединение к host:port напрямую.
func (f *Freedom) Dial(ctx context.Context, network, host string, port int) (net.Conn, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	if f.Dialer != nil {
		return f.Dialer.DialContext(ctx, network, addr)
	}
	return net.Dial(network, addr)
}

func (f *Freedom) Name() string { return FreedomOutboundName }
