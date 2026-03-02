package inbound

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"sync"
)

const (
	socks5Version     = 0x05
	socks5CmdConnect  = 0x01
	socks5AtypIPv4    = 0x01
	socks5AtypDomain  = 0x03
	socks5AtypIPv6    = 0x04
	socks5RepSuccess  = 0x00
	socks5RepFailure  = 0x01
)

// ServerSOCKS5 — inbound SOCKS5-прокси (RFC 1928). Поддерживает CONNECT без аутентификации.
type ServerSOCKS5 struct {
	Outbound Outbound
	Log      *log.Logger
}

// NewServerSOCKS5 создаёт SOCKS5-сервер с заданным outbound для исходящих соединений.
func NewServerSOCKS5(outbound Outbound) *ServerSOCKS5 {
	return &ServerSOCKS5{Outbound: outbound, Log: log.Default()}
}

// ListenAndServe слушает addr (например "127.0.0.1:1080") и обрабатывает SOCKS5-клиентов.
func (s *ServerSOCKS5) ListenAndServe(ctx context.Context, addr string) error {
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("socks5 listen %s: %w", addr, err)
	}
	defer ln.Close()
	s.Log.Printf("socks5: listening on %s (outbound: %s)", addr, s.Outbound.Name())
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			s.Log.Printf("socks5 accept: %v", err)
			continue
		}
		go s.handle(conn)
	}
}

func (s *ServerSOCKS5) handle(conn net.Conn) {
	defer conn.Close()
	// 1) Greeting: [VER, NMETHODS, METHODS]
	buf := make([]byte, 257)
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	if buf[0] != socks5Version {
		return
	}
	nmethods := int(buf[1])
	if nmethods <= 0 || nmethods > 255 {
		if _, err := io.ReadFull(conn, buf[:nmethods]); err != nil {
			return
		}
	}
	if _, err := io.ReadFull(conn, buf[:nmethods]); err != nil {
		return
	}
	// Ответ: no auth
	if _, err := conn.Write([]byte{socks5Version, 0x00}); err != nil {
		return
	}
	// 2) Request: [VER, CMD, RSV, ATYP, DST.ADDR, DST.PORT]
	if _, err := io.ReadFull(conn, buf[:4]); err != nil {
		return
	}
	if buf[0] != socks5Version || buf[1] != socks5CmdConnect {
		s.sendReply(conn, socks5RepFailure)
		return
	}
	atyp := buf[3]
	var host string
	switch atyp {
	case socks5AtypIPv4:
		if _, err := io.ReadFull(conn, buf[:4]); err != nil {
			return
		}
		host = net.IP(buf[:4]).String()
	case socks5AtypIPv6:
		if _, err := io.ReadFull(conn, buf[:16]); err != nil {
			return
		}
		host = net.IP(buf[:16]).String()
	case socks5AtypDomain:
		if _, err := io.ReadFull(conn, buf[:1]); err != nil {
			return
		}
		domainLen := int(buf[0])
		if domainLen <= 0 || domainLen > 255 {
			s.sendReply(conn, socks5RepFailure)
			return
		}
		if _, err := io.ReadFull(conn, buf[:domainLen]); err != nil {
			return
		}
		host = string(buf[:domainLen])
	default:
		s.sendReply(conn, socks5RepFailure)
		return
	}
	if _, err := io.ReadFull(conn, buf[:2]); err != nil {
		return
	}
	port := int(binary.BigEndian.Uint16(buf[:2]))
	targetConn, err := s.Outbound.Dial(context.Background(), "tcp", host, port)
	if err != nil {
		s.sendReply(conn, socks5RepFailure)
		s.Log.Printf("socks5 dial %s:%d: %v", host, port, err)
		return
	}
	defer targetConn.Close()
	if err := s.sendReply(conn, socks5RepSuccess); err != nil {
		return
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { io.Copy(targetConn, conn); wg.Done() }()
	go func() { io.Copy(conn, targetConn); wg.Done() }()
	wg.Wait()
}

func (s *ServerSOCKS5) sendReply(conn net.Conn, rep byte) error {
	// [VER, REP, RSV, ATYP=0x01, BND.ADDR 4 bytes, BND.PORT 2 bytes]
	msg := []byte{socks5Version, rep, 0x00, socks5AtypIPv4, 0, 0, 0, 0, 0, 0}
	_, err := conn.Write(msg)
	return err
}
