package inbound

import (
	"bufio"
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

func TestHTTPProxy_ConnectSuccess(t *testing.T) {
	clientPipe, targetPipe := net.Pipe()
	mock := &mockOutbound{Return: targetPipe}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	srv := NewServerHTTPProxy(mock)
	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			srv.handle(conn)
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// CONNECT request
	req := "CONNECT example.com:80 HTTP/1.1\r\nHost: example.com:80\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(line, "HTTP/1.1 200") {
		t.Errorf("expected HTTP/1.1 200 Connection Established, got %q", strings.TrimSpace(line))
	}
	// Дочитываем заголовки до \r\n\r\n
	for {
		l, _ := br.ReadString('\n')
		if l == "\r\n" || l == "\n" {
			break
		}
	}

	mock.mu.Lock()
	calls := append([]struct{ Network, Host string; Port int }{}, mock.Calls...)
	mock.mu.Unlock()
	if len(calls) != 1 || calls[0].Host != "example.com" || calls[0].Port != 80 {
		t.Errorf("expected one Dial(example.com, 80), got %+v", calls)
	}

	_ = clientPipe.Close()
}

func TestHTTPProxy_ConnectDialFails(t *testing.T) {
	srv := NewServerHTTPProxy(NewBlackhole())
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			srv.handle(conn)
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))

	req := "CONNECT example.com:80 HTTP/1.1\r\nHost: example.com:80\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "502") {
		t.Errorf("expected 502 Bad Gateway when outbound fails, got %q", strings.TrimSpace(line))
	}
}

func TestHTTPProxy_NonConnectMethod(t *testing.T) {
	mock := &mockOutbound{}
	srv := NewServerHTTPProxy(mock)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	go func() {
		conn, _ := ln.Accept()
		if conn != nil {
			srv.handle(conn)
		}
	}()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(time.Second))

	// GET instead of CONNECT
	req := "GET http://example.com/ HTTP/1.1\r\nHost: example.com\r\n\r\n"
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatal(err)
	}

	br := bufio.NewReader(conn)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(line, "405") {
		t.Errorf("expected 405 Method Not Allowed, got %q", strings.TrimSpace(line))
	}
	if len(mock.Calls) != 0 {
		t.Errorf("expected no Dial call for GET, got %+v", mock.Calls)
	}
}

// При отмене контекста ListenAndServe возвращает context.Canceled (listener закрывается).
func TestHTTPProxy_ListenAndServe_ContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	srv := NewServerHTTPProxy(NewBlackhole())
	done := make(chan error, 1)
	go func() { done <- srv.ListenAndServe(ctx, "127.0.0.1:0") }()
	cancel()
	err := <-done
	if err != nil && err != context.Canceled {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
