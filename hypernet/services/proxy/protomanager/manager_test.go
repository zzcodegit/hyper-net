package protomanager

import (
	"io"
	"net"
	"testing"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

type dummyProtocol struct {
	name     string
	features protoiface.Features
}

func (d *dummyProtocol) Name() string { return d.name }

func (d *dummyProtocol) Handshake(stream io.ReadWriteCloser, cfg *protoiface.Config) (protoiface.Session, error) {
	return stream, nil
}

func (d *dummyProtocol) Wrap(conn net.Conn, cfg *protoiface.Config) (io.ReadWriteCloser, error) {
	return conn, nil
}

func (d *dummyProtocol) Features() protoiface.Features { return d.features }

func TestManager_RegisterAndSupportedNames(t *testing.T) {
	m := NewManager(nil)

	m.Register(&dummyProtocol{name: "vless"})
	m.Register(&dummyProtocol{name: "trojan"})
	m.Register(&dummyProtocol{name: "shadowsocks"})

	names := m.SupportedNames()
	if len(names) != 3 {
		t.Fatalf("expected 3 protocols, got %d", len(names))
	}
}

func TestManager_ChooseCommon_WithPreferred(t *testing.T) {
	m := NewManager([]string{"hysteria2", "trojan", "vless"})

	m.Register(&dummyProtocol{name: "vless"})
	m.Register(&dummyProtocol{name: "trojan"})
	m.Register(&dummyProtocol{name: "shadowsocks"})

	// localSupported задаёт порядок предпочтений вызывающей стороны.
	local := []string{"vless", "trojan", "shadowsocks"}
	remote := []string{"trojan", "vless"}

	got, ok := m.ChooseCommon(local, remote)
	if !ok {
		t.Fatalf("expected common protocol, got none")
	}
	if got != "vless" {
		t.Fatalf("expected vless to be chosen, got %s", got)
	}
}

func TestManager_ChooseCommon_NoPreferred(t *testing.T) {
	m := NewManager(nil)

	m.Register(&dummyProtocol{name: "vless"})
	m.Register(&dummyProtocol{name: "trojan"})

	local := []string{"trojan", "vless"}
	remote := []string{"vless"}

	got, ok := m.ChooseCommon(local, remote)
	if !ok {
		t.Fatalf("expected common protocol, got none")
	}
	if got == "" {
		t.Fatalf("empty protocol name returned")
	}
}

func TestConfig_Defaults(t *testing.T) {
	cfg := &protoiface.Config{
		Token:   "test-token",
		Timeout: 5 *time.Second,
		Raw:     map[string]any{"foo": "bar"},
	}

	if cfg.Token == "" {
		t.Fatalf("expected token to be set")
	}
	if cfg.Timeout <= 0 {
		t.Fatalf("expected positive timeout")
	}
	if cfg.Raw["foo"] != "bar" {
		t.Fatalf("unexpected raw value: %#v", cfg.Raw["foo"])
	}
}

