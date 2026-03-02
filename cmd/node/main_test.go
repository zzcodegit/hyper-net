package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"hypernet-node/pkg/config"
	"hypernet-node/hypernet/node/daemon"
)

// TestRun_Smoke запускает run с минимальным конфигом и отменой контекста через ~2 с.
// Проверяет, что нода поднимается без паники и корректно завершается по ctx.
func TestRun_Smoke(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping smoke test in short mode")
	}
	dir := t.TempDir()
	cfg := &config.Config{
		Port:        0,
		KeyPath:     filepath.Join(dir, "node.key"),
		EnableDHT:   false,
		EnableRelay: false,
		DisableQUIC: true,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := daemon.Run(ctx, cfg)
	if err != nil && err != context.DeadlineExceeded && err != context.Canceled {
		t.Fatalf("run: %v", err)
	}
}
