package inbound

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"
)

// mockOutbound для dokodemo: записывает вызовы Dial (host/port от dokodemo — пустые).
type dokodemoMockOutbound struct {
	mu    sync.Mutex
	Calls []struct{ Network, Host string; Port int }
	Return net.Conn
	Err    error
}

func (m *dokodemoMockOutbound) Dial(ctx context.Context, network, host string, port int) (net.Conn, error) {
	m.mu.Lock()
	m.Calls = append(m.Calls, struct{ Network, Host string; Port int }{network, host, port})
	ret, err := m.Return, m.Err
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (m *dokodemoMockOutbound) Name() string { return "dokodemo-mock" }

func TestDokodemo_DialArgs(t *testing.T) {
	clientPipe, targetPipe := net.Pipe()
	mock := &dokodemoMockOutbound{Return: targetPipe}
	door := NewDokodemoDoor(mock)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			door.handle(conn)
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	conn.Close() // закрываем сразу — Dial уже вызван, relay получит EOF

	time.Sleep(50 * time.Millisecond) // даём handle вызвать Dial
	mock.mu.Lock()
	calls := append([]struct{ Network, Host string; Port int }{}, mock.Calls...)
	mock.mu.Unlock()
	if len(calls) != 1 {
		t.Fatalf("expected one Dial call, got %d", len(calls))
	}
	if calls[0].Network != "tcp" || calls[0].Host != "" || calls[0].Port != 0 {
		t.Errorf("expected Dial(tcp, \"\", 0), got %+v", calls[0])
	}
	_ = clientPipe.Close()
}

func TestDokodemo_RelaySuccess(t *testing.T) {
	// Слушаем "цель" — dokodemo будет ретранслировать сюда через Redirect
	lnTarget, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer lnTarget.Close()

	r, err := NewRedirect(lnTarget.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	door := NewDokodemoDoor(r)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			door.handle(conn)
		}
	}()

	// На "цели": принять соединение от dokodemo, прочитать "hello", ответить "world"
	gotHello := make(chan string, 1)
	go func() {
		targetConn, err := lnTarget.Accept()
		if err != nil {
			return
		}
		defer targetConn.Close()
		_ = targetConn.SetDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 5)
		if n, err := targetConn.Read(buf); err == nil {
			gotHello <- string(buf[:n])
		}
		if _, err := targetConn.Write([]byte("world")); err != nil {
			return
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got := <-gotHello; got != "hello" {
		t.Errorf("target received %q, want hello", got)
	}
	buf := make([]byte, 5)
	if _, err := conn.Read(buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "world" {
		t.Errorf("client received %q, want world", buf)
	}
}

func TestDokodemo_DialFails(t *testing.T) {
	door := NewDokodemoDoor(NewBlackhole())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			door.handle(conn)
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))

	// handle закрывает conn при ошибке Dial — читаем до EOF
	buf := make([]byte, 10)
	_, err = conn.Read(buf)
	if err == nil {
		t.Error("expected connection close (read error) when outbound fails")
	}
}

// При отмене контекста ListenAndServe возвращает context.Canceled.
func TestDokodemo_ListenAndServe_ContextCancel(t *testing.T) {
	r, err := NewRedirect("127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	door := NewDokodemoDoor(r)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- door.ListenAndServe(ctx, "127.0.0.1:0") }()
	cancel()
	err = <-done
	if err != nil && err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
