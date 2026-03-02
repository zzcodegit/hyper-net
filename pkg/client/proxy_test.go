package client

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"hypernet-node/pkg/config"
	hypernode "hypernet-node/hypernet/core/network/node"
	proxy "hypernet-node/hypernet/services/proxy/server"
)

type memStream struct {
	io.ReadWriter
	closed bool
}

func (m *memStream) Close() error {
	m.closed = true
	return nil
}

func TestUDPTunnelFraming(t *testing.T) {
	buf := &bytes.Buffer{}
	s := &memStream{ReadWriter: buf}
	tunnel := &udpTunnel{
		stream: s,
		r:      bufio.NewReader(buf),
	}

	// Записываем датаграмму.
	n, err := tunnel.Write([]byte("hello"))
	if err != nil {
		t.Fatalf("udpTunnel.Write error: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected to write 5 bytes, wrote %d", n)
	}

	// Теперь читаем её обратно.
	out := make([]byte, 16)
	readN, err := tunnel.Read(out)
	if err != nil {
		t.Fatalf("udpTunnel.Read error: %v", err)
	}
	if readN != 5 {
		t.Fatalf("expected to read 5 bytes, got %d", readN)
	}
	if string(out[:readN]) != "hello" {
		t.Fatalf("expected 'hello', got %q", string(out[:readN]))
	}
}

func TestProxyTCPIntegration(t *testing.T) {
	t.Skip("disabled in automated suite; covered by manual/integration runs")
	// Локальный TCP echo-сервер.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	defer ln.Close()

	echoDone := make(chan struct{})
	go func() {
		defer close(echoDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(conn, conn)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Нода A с proxy-хэндлером.
	aCfg := &config.Config{Port: 0, KeyPath: "proxy-a.key"}
	nodeA, err := hypernode.New(ctx, aCfg)
	if err != nil {
		t.Fatalf("new nodeA: %v", err)
	}
	defer nodeA.Close()
	proxyServer := proxy.NewServer()
	proxyServer.Register(nodeA.Host)

	// Нода B как клиент.
	bCfg := &config.Config{Port: 0, KeyPath: "proxy-b.key"}
	nodeB, err := hypernode.New(ctx, bCfg)
	if err != nil {
		t.Fatalf("new nodeB: %v", err)
	}
	defer nodeB.Close()

	// Multiaddr ноды A.
	addrs := nodeA.Host.Addrs()
	if len(addrs) == 0 {
		t.Fatalf("nodeA has no addrs")
	}
	targetMaddr := addrs[0].String() + "/p2p/" + nodeA.Host.ID().String()

	pc, err := NewProxyClient(nodeB.Host, targetMaddr, "")
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	stream, err := pc.DialTCP(ctx, host, port)
	if err != nil {
		t.Fatalf("DialTCP: %v", err)
	}
	defer stream.Close()

	if _, err := io.WriteString(stream, "hello"); err != nil {
		t.Fatalf("write via proxy: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := io.ReadFull(stream, buf); err != nil {
		t.Fatalf("read via proxy: %v", err)
	}
	if string(buf) != "hello" {
		t.Fatalf("expected echo 'hello', got %q", string(buf))
	}
	<-echoDone
}

func TestProxyTCPFileSizes(t *testing.T) {
	t.Skip("disabled in automated suite; covered by manual/integration runs")
	sizes := []int{1, 1024, 1024 * 1024, 10 * 1024 * 1024}

	for _, size := range sizes {
		t.Run(fmt.Sprintf("%d-bytes", size), func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("tcp listen: %v", err)
			}
			defer ln.Close()

			echoDone := make(chan struct{})
			go func() {
				defer close(echoDone)
				conn, err := ln.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			aCfg := &config.Config{Port: 0, KeyPath: "proxy-a.key"}
			nodeA, err := hypernode.New(ctx, aCfg)
			if err != nil {
				t.Fatalf("new nodeA: %v", err)
			}
			defer nodeA.Close()
			proxyServer := proxy.NewServer()
			proxyServer.Register(nodeA.Host)

			bCfg := &config.Config{Port: 0, KeyPath: "proxy-b.key"}
			nodeB, err := hypernode.New(ctx, bCfg)
			if err != nil {
				t.Fatalf("new nodeB: %v", err)
			}
			defer nodeB.Close()

			addrs := nodeA.Host.Addrs()
			if len(addrs) == 0 {
				t.Fatalf("nodeA has no addrs")
			}
			targetMaddr := addrs[0].String() + "/p2p/" + nodeA.Host.ID().String()

			pc, err := NewProxyClient(nodeB.Host, targetMaddr, "")
			if err != nil {
				t.Fatalf("NewProxyClient: %v", err)
			}

			host, portStr, _ := net.SplitHostPort(ln.Addr().String())
			port, _ := strconv.Atoi(portStr)

			stream, err := pc.DialTCP(ctx, host, port)
			if err != nil {
				t.Fatalf("DialTCP: %v", err)
			}

			src := make([]byte, size)
			if _, err := rand.Read(src); err != nil {
				t.Fatalf("rand.Read: %v", err)
			}

			if _, err := stream.Write(src); err != nil {
				t.Fatalf("write via proxy: %v", err)
			}

			dst := make([]byte, size)
			if _, err := io.ReadFull(stream, dst); err != nil {
				t.Fatalf("read via proxy: %v", err)
			}
			_ = stream.Close()
			<-echoDone

			if !bytes.Equal(src, dst) {
				t.Fatalf("mismatch between sent and received buffers")
			}
		})
	}
}

func TestProxyParallelConnections(t *testing.T) {
	t.Skip("disabled in automated suite; covered by manual/integration runs")
	const parallel = 50

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("tcp listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	aCfg := &config.Config{Port: 0, KeyPath: "proxy-a.key"}
	nodeA, err := hypernode.New(ctx, aCfg)
	if err != nil {
		t.Fatalf("new nodeA: %v", err)
	}
	defer nodeA.Close()
	proxyServer := proxy.NewServer()
	proxyServer.Register(nodeA.Host)

	bCfg := &config.Config{Port: 0, KeyPath: "proxy-b.key"}
	nodeB, err := hypernode.New(ctx, bCfg)
	if err != nil {
		t.Fatalf("new nodeB: %v", err)
	}
	defer nodeB.Close()

	addrs := nodeA.Host.Addrs()
	if len(addrs) == 0 {
		t.Fatalf("nodeA has no addrs")
	}
	targetMaddr := addrs[0].String() + "/p2p/" + nodeA.Host.ID().String()

	pc, err := NewProxyClient(nodeB.Host, targetMaddr, "")
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	host, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)

	errCh := make(chan error, parallel)
	for i := 0; i < parallel; i++ {
		go func() {
			stream, err := pc.DialTCP(ctx, host, port)
			if err != nil {
				errCh <- err
				return
			}
			defer stream.Close()

			if _, err := io.WriteString(stream, "hello"); err != nil {
				errCh <- err
				return
			}
			buf := make([]byte, 5)
			if _, err := io.ReadFull(stream, buf); err != nil {
				errCh <- err
				return
			}
			if string(buf) != "hello" {
				errCh <- fmt.Errorf("expected 'hello', got %q", string(buf))
				return
			}
			errCh <- nil
		}()
	}

	for i := 0; i < parallel; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("parallel connection failed: %v", err)
		}
	}
}

func TestProxyDomainHTTP(t *testing.T) {
	if os.Getenv("PROXY_DOMAIN_TESTS") != "1" {
		t.Skip("set PROXY_DOMAIN_TESTS=1 to run external domain tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	aCfg := &config.Config{Port: 0, KeyPath: "proxy-a.key"}
	nodeA, err := hypernode.New(ctx, aCfg)
	if err != nil {
		t.Fatalf("new nodeA: %v", err)
	}
	defer nodeA.Close()
	proxyServer := proxy.NewServer()
	proxyServer.Register(nodeA.Host)

	bCfg := &config.Config{Port: 0, KeyPath: "proxy-b.key"}
	nodeB, err := hypernode.New(ctx, bCfg)
	if err != nil {
		t.Fatalf("new nodeB: %v", err)
	}
	defer nodeB.Close()

	addrs := nodeA.Host.Addrs()
	if len(addrs) == 0 {
		t.Fatalf("nodeA has no addrs")
	}
	targetMaddr := addrs[0].String() + "/p2p/" + nodeA.Host.ID().String()

	pc, err := NewProxyClient(nodeB.Host, targetMaddr, "")
	if err != nil {
		t.Fatalf("NewProxyClient: %v", err)
	}

	// HTTP to google.com:80
	stream, err := pc.DialTCP(ctx, "google.com", 80)
	if err != nil {
		t.Fatalf("DialTCP google.com:80 via proxy: %v", err)
	}
	defer stream.Close()

	if _, err := io.WriteString(stream, "GET / HTTP/1.0\r\nHost: google.com\r\n\r\n"); err != nil {
		t.Fatalf("write http request: %v", err)
	}
	br := bufio.NewReader(stream)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read http status line: %v", err)
	}
	if !strings.HasPrefix(line, "HTTP/1.") {
		t.Fatalf("unexpected HTTP status line: %q", line)
	}

	// TLS to github.com:443
	stream2, err := pc.DialTCP(ctx, "github.com", 443)
	if err != nil {
		t.Fatalf("DialTCP github.com:443 via proxy: %v", err)
	}
	defer stream2.Close()

	conn := tls.Client(&streamConn{ReadWriteCloser: stream2}, &tls.Config{
		ServerName: "github.com",
	})
	if err := conn.Handshake(); err != nil {
		t.Fatalf("TLS handshake with github.com via proxy failed: %v", err)
	}
	_ = conn.Close()
}

// streamConn оборачивает io.ReadWriteCloser в net.Conn для TLS.
type streamConn struct {
	io.ReadWriteCloser
}

func (s *streamConn) LocalAddr() net.Addr  { return dummyAddr("local") }
func (s *streamConn) RemoteAddr() net.Addr { return dummyAddr("remote") }
func (s *streamConn) SetDeadline(t time.Time) error {
	return nil
}
func (s *streamConn) SetReadDeadline(t time.Time) error {
	return nil
}
func (s *streamConn) SetWriteDeadline(t time.Time) error {
	return nil
}

type dummyAddr string

func (d dummyAddr) Network() string { return string(d) }
func (d dummyAddr) String() string  { return string(d) }


