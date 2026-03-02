package vmess

import (
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"hypernet-node/hypernet/services/proxy/protoiface"
)

func TestVMESSProtocol_NameAndFeatures(t *testing.T) {
	p := &VMESSProtocol{}
	if p.Name() != ProtocolName {
		t.Errorf("Name() = %q, want %q", p.Name(), ProtocolName)
	}
	feat := p.Features()
	if len(feat.Transports) == 0 {
		t.Error("expected non-empty Transports")
	}
	if feat.Encryption == "" {
		t.Error("expected Encryption description")
	}
}

func TestVMESSHandshake_Success(t *testing.T) {
	userID := uuid.New()
	token := userID.String()

	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	p := &VMESSProtocol{}
	serverCfg := &protoiface.Config{
		Token:   token,
		Timeout: 5 * time.Second,
		Raw:     map[string]any{"role": "server"},
	}
	clientCfg := &protoiface.Config{
		Token:   token,
		Timeout: 5 * time.Second,
		Raw:     map[string]any{"role": "client"},
	}

	errCh := make(chan error, 1)
	go func() {
		_, err := p.Handshake(serverSide, serverCfg)
		errCh <- err
	}()

	if _, err := p.Handshake(clientSide, clientCfg); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("server handshake: %v", err)
	}
}

func TestVMESSHandshake_InvalidConfig(t *testing.T) {
	p := &VMESSProtocol{}
	clientSide, serverSide := net.Pipe()
	defer clientSide.Close()
	defer serverSide.Close()

	t.Run("nil config", func(t *testing.T) {
		_, err := p.Handshake(clientSide, nil)
		if err == nil {
			t.Fatal("expected error for nil config")
		}
	})

	t.Run("missing role", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		_, err := p.Handshake(c1, &protoiface.Config{Token: uuid.New().String(), Raw: map[string]any{}})
		if err == nil {
			t.Fatal("expected error when role missing")
		}
	})

	t.Run("invalid UUID token", func(t *testing.T) {
		c1, c2 := net.Pipe()
		defer c1.Close()
		defer c2.Close()
		_, err := p.Handshake(c1, &protoiface.Config{
			Token: "not-a-uuid",
			Raw:   map[string]any{"role": "client"},
		})
		if err == nil {
			t.Fatal("expected error for invalid UUID")
		}
	})
}

func TestVMESSWrap(t *testing.T) {
	p := &VMESSProtocol{}
	conn, _ := net.Pipe()
	defer conn.Close()
	rwc, err := p.Wrap(conn, &protoiface.Config{})
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	if rwc != conn {
		t.Error("Wrap with nil config should return same conn")
	}
}
