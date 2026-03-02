package inbound

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
)

// ServerHTTPProxy — inbound HTTP-прокси (метод CONNECT). Клиент шлёт "CONNECT host:port HTTP/1.1".
type ServerHTTPProxy struct {
	Outbound Outbound
	Log      *log.Logger
}

// NewServerHTTPProxy создаёт HTTP CONNECT прокси с заданным outbound.
func NewServerHTTPProxy(outbound Outbound) *ServerHTTPProxy {
	return &ServerHTTPProxy{Outbound: outbound, Log: log.Default()}
}

// ListenAndServe слушает addr (например "127.0.0.1:8080") и обрабатывает CONNECT-запросы.
func (s *ServerHTTPProxy) ListenAndServe(ctx context.Context, addr string) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("http proxy listen %s: %w", addr, err)
	}
	defer ln.Close()
	s.Log.Printf("http proxy: listening on %s (outbound: %s)", addr, s.Outbound.Name())
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
			s.Log.Printf("http proxy accept: %v", err)
			continue
		}
		go s.handle(conn)
	}
}

func (s *ServerHTTPProxy) handle(conn net.Conn) {
	defer conn.Close()
	br := bufio.NewReader(conn)
	req, err := http.ReadRequest(br)
	if err != nil {
		s.Log.Printf("http proxy read request: %v", err)
		return
	}
	if req.Method != http.MethodConnect {
		s.writeStatus(conn, http.StatusMethodNotAllowed, "Method Not Allowed")
		return
	}
	host, port, err := parseHostPort(req.Host)
	if err != nil {
		s.writeStatus(conn, http.StatusBadRequest, "Bad Request: invalid host:port")
		return
	}
	targetConn, err := s.Outbound.Dial(context.Background(), "tcp", host, port)
	if err != nil {
		s.Log.Printf("http proxy dial %s:%d: %v", host, port, err)
		s.writeStatus(conn, http.StatusBadGateway, "Bad Gateway")
		return
	}
	defer targetConn.Close()
	// Ответ 200 и далее туннель
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	// Остаток буфера (тело запроса) отдаём в target
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		if br.Buffered() > 0 {
			io.Copy(targetConn, br)
		}
		io.Copy(targetConn, conn)
		wg.Done()
	}()
	go func() {
		io.Copy(conn, targetConn)
		wg.Done()
	}()
	wg.Wait()
}

func parseHostPort(hostPort string) (host string, port int, err error) {
	hostPort = strings.TrimSpace(hostPort)
	if hostPort == "" {
		return "", 0, fmt.Errorf("empty host")
	}
	if idx := strings.LastIndex(hostPort, ":"); idx >= 0 {
		host = hostPort[:idx]
		p, e := strconv.Atoi(hostPort[idx+1:])
		if e != nil || p <= 0 || p > 65535 {
			return "", 0, fmt.Errorf("invalid port")
		}
		return host, p, nil
	}
	return hostPort, 443, nil
}

func (s *ServerHTTPProxy) writeStatus(conn net.Conn, code int, text string) {
	io.WriteString(conn, fmt.Sprintf("HTTP/1.1 %d %s\r\nContent-Length: 0\r\n\r\n", code, text))
}
