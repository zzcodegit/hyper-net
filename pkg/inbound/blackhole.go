package inbound

import (
	"context"
	"errors"
	"net"
)

const BlackholeOutboundName = "blackhole"

var errBlackhole = errors.New("blackhole: traffic dropped")

// Blackhole — исходящий, который не устанавливает соединение и отбрасывает трафик (как Xray Blackhole).
type Blackhole struct{}

// NewBlackhole создаёт outbound Blackhole.
func NewBlackhole() *Blackhole { return &Blackhole{} }

// Dial всегда возвращает ошибку; соединение не устанавливается.
func (b *Blackhole) Dial(ctx context.Context, network, host string, port int) (net.Conn, error) {
	return nil, errBlackhole
}

func (b *Blackhole) Name() string { return BlackholeOutboundName }
