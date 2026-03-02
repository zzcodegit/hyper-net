package identity

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/libp2p/go-libp2p/core/peer"
)

func TestLoadOrCreatePrivateKey_EmptyPath(t *testing.T) {
	_, err := LoadOrCreatePrivateKey("")
	if err == nil {
		t.Fatal("expected error for empty key path")
	}
	if err.Error() != "empty key path" {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestLoadOrCreatePrivateKey_NewKeyCreatesFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.key")

	priv, err := LoadOrCreatePrivateKey(path)
	if err != nil {
		t.Fatalf("LoadOrCreatePrivateKey: %v", err)
	}
	if priv == nil {
		t.Fatal("expected non-nil private key")
	}

	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("key file was not created")
	}
}

func TestLoadOrCreatePrivateKey_ExistingKeyLoadsSame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.key")

	priv1, err := LoadOrCreatePrivateKey(path)
	if err != nil {
		t.Fatalf("first LoadOrCreatePrivateKey: %v", err)
	}
	id1, err := peer.IDFromPublicKey(priv1.GetPublic())
	if err != nil {
		t.Fatalf("IDFromPublicKey(priv1): %v", err)
	}

	priv2, err := LoadOrCreatePrivateKey(path)
	if err != nil {
		t.Fatalf("second LoadOrCreatePrivateKey: %v", err)
	}
	id2, err := peer.IDFromPublicKey(priv2.GetPublic())
	if err != nil {
		t.Fatalf("IDFromPublicKey(priv2): %v", err)
	}

	if id1 != id2 {
		t.Errorf("same key path should yield same peer ID: %s != %s", id1, id2)
	}
}

func TestLoadOrCreatePrivateKey_NonexistentDirReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent", "sub", "node.key")
	_, err := LoadOrCreatePrivateKey(path)
	if err == nil {
		t.Fatal("expected error when parent dir does not exist")
	}
}

