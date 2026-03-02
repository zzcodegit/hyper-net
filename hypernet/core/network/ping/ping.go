package ping

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

const (
	// ProtocolID is the custom ping protocol identifier.
	ProtocolID = "/ping/1.0.0"

	// newline-terminated payload to avoid blocking on EOF.
	defaultPayload = "ping\n"
)

// Register installs the ping handler on the given host.
func Register(h host.Host) {
	h.SetStreamHandler(ProtocolID, handlePing)
}

func handlePing(s network.Stream) {
	defer s.Close()

	reader := bufio.NewReader(s)
	payload, err := reader.ReadString('\n')
	if err != nil {
		return
	}

	// Echo back the same payload.
	_, _ = io.WriteString(s, payload)
}

// PingTarget dials the target multiaddr (with /p2p/PeerID), opens a stream using the ping protocol
// and sends count ping messages, printing RTT information to stdout.
func PingTarget(ctx context.Context, h host.Host, targetAddr string, count int) error {
	if count <= 0 {
		count = 1
	}

	maddr, err := multiaddr.NewMultiaddr(targetAddr)
	if err != nil {
		return fmt.Errorf("invalid target multiaddr: %w", err)
	}

	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return fmt.Errorf("parse peer info: %w", err)
	}

	if err := h.Connect(ctx, *info); err != nil {
		return fmt.Errorf("connect to peer: %w", err)
	}

	for i := 0; i < count; i++ {
		start := time.Now()

		stream, err := h.NewStream(ctx, info.ID, ProtocolID)
		if err != nil {
			return fmt.Errorf("open stream: %w", err)
		}

		if _, err := io.WriteString(stream, defaultPayload); err != nil {
			_ = stream.Close()
			return fmt.Errorf("write ping: %w", err)
		}

		reader := bufio.NewReader(stream)
		reply, err := reader.ReadString('\n')
		_ = stream.Close()
		if err != nil {
			return fmt.Errorf("read pong: %w", err)
		}

		rtt := time.Since(start)
		fmt.Printf("ping %d: rtt=%s payload=%q\n", i+1, rtt, string(reply))
	}

	return nil
}

