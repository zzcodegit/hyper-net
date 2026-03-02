package inbound

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// mockOutbound записывает вызовы Dial и возвращает заданное соединение или ошибку.
type mockOutbound struct {
	mu     sync.Mutex
	Calls  []struct{ Network, Host string; Port int }
	Return net.Conn
	Err    error
	Name_  string
}

func (m *mockOutbound) Dial(ctx context.Context, network, host string, port int) (net.Conn, error) {
	m.mu.Lock()
	m.Calls = append(m.Calls, struct{ Network, Host string; Port int }{network, host, port})
	ret, err := m.Return, m.Err
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return ret, nil
}

func (m *mockOutbound) Name() string {
	if m.Name_ != "" {
		return m.Name_
	}
	return "mock"
}

// Отправляет SOCKS5 greeting (no auth) и CONNECT request по домену.
func writeSOCKS5ConnectDomain(conn net.Conn, host string, port uint16) error {
	// Greeting: VER=5, NMETHODS=1, METHODS=0 (no auth)
	if _, err := conn.Write([]byte{socks5Version, 0x01, 0x00}); err != nil {
		return err
	}
	// Request: VER, CMD=CONNECT, RSV=0, ATYP=domain, DST.ADDR, DST.PORT
	domain := []byte(host)
	if len(domain) > 255 {
		domain = domain[:255]
	}
	req := make([]byte, 0, 4+1+len(domain)+2)
	req = append(req, socks5Version, socks5CmdConnect, 0x00, socks5AtypDomain)
	req = append(req, byte(len(domain)))
	req = append(req, domain...)
	portB := make([]byte, 2)
	binary.BigEndian.PutUint16(portB, port)
	req = append(req, portB...)
	_, err := conn.Write(req)
	return err
}

func TestSOCKS5_ConnectSuccess(t *testing.T) {
	// Mock возвращает конец pipe, чтобы handle мог io.Copy; второй конец закроем после проверки ответа.
	clientPipe, targetPipe := net.Pipe()
	mock := &mockOutbound{Return: targetPipe}

	srv := NewServerSOCKS5(mock)
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
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))

	// Greeting + CONNECT example.com:80
	if err := writeSOCKS5ConnectDomain(conn, "example.com", 80); err != nil {
		t.Fatal(err)
	}

	// Пропускаем ответ на greeting (auth): 2 байта
	authReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, authReply); err != nil {
		t.Fatal(err)
	}
	// Читаем ответ на CONNECT: VER, REP (0x00 = success), ...
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[0] != socks5Version || reply[1] != socks5RepSuccess {
		t.Errorf("expected SOCKS5 success reply, got VER=%d REP=%d", reply[0], reply[1])
	}

	mock.mu.Lock()
	calls := append([]struct{ Network, Host string; Port int }{}, mock.Calls...)
	mock.mu.Unlock()
	if len(calls) != 1 || calls[0].Host != "example.com" || calls[0].Port != 80 {
		t.Errorf("expected one Dial(example.com, 80), got %+v", calls)
	}

	_ = clientPipe.Close()
}

func TestSOCKS5_ConnectDialFails(t *testing.T) {
	srv := NewServerSOCKS5(NewBlackhole())
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

	if err := writeSOCKS5ConnectDomain(conn, "example.com", 80); err != nil {
		t.Fatal(err)
	}

	authReply := make([]byte, 2)
	if _, err := io.ReadFull(conn, authReply); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[1] != socks5RepFailure {
		t.Errorf("expected SOCKS5 failure reply when outbound fails, got REP=%d", reply[1])
	}
}
