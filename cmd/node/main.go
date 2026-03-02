package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"hypernet-node/pkg/config"
	"hypernet-node/hypernet/node/daemon"

	_ "net/http/pprof"
)

func startPprof() {
	pprofAddr := os.Getenv("PPROF_ADDR")
	if pprofAddr == "" {
		// Только localhost по умолчанию, чтобы не светить профайлер наружу.
		pprofAddr = "127.0.0.1:6060"
	}
	go func() {
		if err := http.ListenAndServe(pprofAddr, nil); err != nil {
			log.Printf("pprof server error on %s: %v", pprofAddr, err)
		}
	}()
}

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received signal %s, shutting down...", sig)
		cancel()
	}()

	if os.Getenv("PPROF_ENABLE") == "1" {
		startPprof()
	}
	if err := daemon.Run(ctx, cfg); err != nil {
		log.Fatalf("run: %v", err)
	}
}

