package config

import (
	"os"
	"testing"
)

func TestNormalizeMode(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"server", "server"},
		{"SERVER", "server"},
		{"client", "client"},
		{"CLIENT", "client"},
		{"auto", "auto"},
		{"", "auto"},
		{"unknown", "auto"},
	}

	for _, tt := range tests {
		if got := normalizeMode(tt.in); got != tt.want {
			t.Errorf("normalizeMode(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestLoadReadsEnvDefaults(t *testing.T) {
	t.Setenv("PORT", "5001")
	t.Setenv("KEY_PATH", "custom.key")
	t.Setenv("RELAY", "true")
	t.Setenv("DHT", "true")
	t.Setenv("DHT_MODE", "server")
	t.Setenv("BOOTSTRAP_PEERS", "/ip4/127.0.0.1/tcp/4001/p2p/QmTestPeer")

	// Ensure .env loading doesn't interfere.
	_ = os.Unsetenv("GO_ENV")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() returned error: %v", err)
	}

	if cfg.Port != 5001 {
		t.Errorf("expected Port=5001, got %d", cfg.Port)
	}
	if cfg.KeyPath != "custom.key" {
		t.Errorf("expected KeyPath=custom.key, got %q", cfg.KeyPath)
	}
	if !cfg.EnableRelay {
		t.Errorf("expected EnableRelay=true")
	}
	if !cfg.EnableDHT {
		t.Errorf("expected EnableDHT=true")
	}
	if cfg.DHTMode != "server" {
		t.Errorf("expected DHTMode=server, got %q", cfg.DHTMode)
	}
	if len(cfg.BootstrapPeers) != 1 {
		t.Fatalf("expected 1 bootstrap peer, got %d", len(cfg.BootstrapPeers))
	}
}

