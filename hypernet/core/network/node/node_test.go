package node

import (
	"context"
	"path/filepath"
	"testing"

	"hypernet-node/pkg/config"
)

func TestNew_EmptyKeyPathFails(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		KeyPath:   "",
		Port:      0,
		EnableDHT: false,
	}
	_, err := New(ctx, cfg)
	if err == nil {
		t.Fatal("New with empty KeyPath should fail")
	}
	if err.Error() == "" {
		t.Error("error message should not be empty")
	}
}

func TestNew_StablePeerIDFromSameKeyFile(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "stable.key")
	ctx := context.Background()

	cfg := &config.Config{
		KeyPath:   keyPath,
		Port:      0,
		EnableDHT: false,
	}

	n1, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("first New: %v", err)
	}
	peerID1 := n1.Host.ID().String()
	n1.Close()

	n2, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("second New: %v", err)
	}
	defer n2.Close()
	peerID2 := n2.Host.ID().String()

	if peerID1 != peerID2 {
		t.Errorf("same key file should yield same Peer ID: %q != %q", peerID1, peerID2)
	}
}

func TestNew_WithRegionTrimsSpace(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "region.key")
	ctx := context.Background()

	cfg := &config.Config{
		KeyPath:   keyPath,
		Port:      0,
		EnableDHT: false,
		Region:    "  eu  ",
	}
	n, err := New(ctx, cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer n.Close()
	if n.Region != "eu" {
		t.Errorf("Region should be trimmed: got %q", n.Region)
	}
}

func TestNode_CloseNilSafe(t *testing.T) {
	var n *Node
	if err := n.Close(); err != nil {
		t.Errorf("Close on nil Node should not return error: %v", err)
	}
}

