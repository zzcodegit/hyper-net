package inbound

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
)

// DokodemoDoor — inbound в стиле Xray dokodemo-door: слушает порт и пересылает весь трафик
// в фиксированный target (redirect). Не анализирует протокол — просто TCP relay.
type DokodemoDoor struct {
	Outbound Outbound // обычно Redirect
	Log      *log.Logger
}

// NewDokodemoDoor создаёт dokodemo-door с заданным outbound (часто NewRedirect(target)).
func NewDokodemoDoor(outbound Outbound) *DokodemoDoor {
	return &DokodemoDoor{Outbound: outbound, Log: log.Default()}
}

// ListenAndServe слушает addr и для каждого входящего соединения открывает одно исходящее
// к target (через outbound) и ретранслирует трафик в обе стороны.
func (d *DokodemoDoor) ListenAndServe(ctx context.Context, addr string) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("dokodemo listen %s: %w", addr, err)
	}
	defer ln.Close()
	d.Log.Printf("dokodemo-door: listening on %s (outbound: %s)", addr, d.Outbound.Name())
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			d.Log.Printf("dokodemo accept: %v", err)
			continue
		}
		go d.handle(conn)
	}
}

func (d *DokodemoDoor) handle(conn net.Conn) {
	defer conn.Close()
	// Dokodemo: подключаемся к фиксированной цели (host/port из outbound).
	targetConn, err := d.Outbound.Dial(context.Background(), "tcp", "", 0)
	if err != nil {
		d.Log.Printf("dokodemo dial: %v", err)
		return
	}
	defer targetConn.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { io.Copy(targetConn, conn); wg.Done() }()
	go func() { io.Copy(conn, targetConn); wg.Done() }()
	wg.Wait()
}
