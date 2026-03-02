package ping

import (
	"context"
	"errors"
	"testing"
	"time"

	libp2p "github.com/libp2p/go-libp2p"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/multiformats/go-multiaddr"
)

// TestPingBetweenHosts поднимает две ноды в памяти и проверяет, что кастомный
// протокол /ping/1.0.0 работает (A -> B -> echo).
func TestPingBetweenHosts(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	hA, err := libp2p.New()
	if err != nil {
		t.Fatalf("create host A: %v", err)
	}
	defer hA.Close()

	hB, err := libp2p.New()
	if err != nil {
		t.Fatalf("create host B: %v", err)
	}
	defer hB.Close()

	// Обработчик ping на B.
	Register(hB)

	target, err := hostToMultiaddr(hB)
	if err != nil {
		t.Fatalf("build target addr: %v", err)
	}

	if err := PingTarget(ctx, hA, target, 1); err != nil {
		t.Fatalf("PingTarget failed: %v", err)
	}
}

func hostToMultiaddr(h host.Host) (string, error) {
	addrs := h.Addrs()
	if len(addrs) == 0 {
		return "", errors.New("host has no listen addrs")
	}
	return addrs[0].Encapsulate(multiaddr.StringCast("/p2p/" + h.ID().String())).String(), nil
}

