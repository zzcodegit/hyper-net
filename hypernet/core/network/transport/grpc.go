package transport

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/multiformats/go-multiaddr"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
)

const GRPCTransportName = "grpc"

// grpcTransport uses HTTP/2 with a simple bidirectional stream (length-prefixed frames).
// Compatible with gRPC-style streaming; no proto codegen required.
type grpcTransport struct{}

func GRPC() Transport { return &grpcTransport{} }

func (t *grpcTransport) Name() string { return GRPCTransportName }

func (t *grpcTransport) Dial(ctx context.Context, addr multiaddr.Multiaddr) (net.Conn, error) {
	host, port, err := parseGRPCMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	addrStr := net.JoinHostPort(host, port)
	tr := &http2.Transport{
		AllowHTTP:          true,
		DisableCompression: true,
	}
	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, "POST", "http://"+addrStr+"/tunnel/stream", pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/grpc")
	req.Header.Set("Te", "trailers")
	req.ContentLength = -1
	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := tr.RoundTrip(req)
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()
	var resp *http.Response
	select {
	case resp = <-respCh:
	case err = <-errCh:
		pw.Close()
		return nil, fmt.Errorf("grpc dial: %w", err)
	case <-ctx.Done():
		pw.Close()
		return nil, ctx.Err()
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		pw.Close()
		return nil, fmt.Errorf("grpc: status %s", resp.Status)
	}
	return &h2StreamConn{ctx: ctx, r: resp.Body, w: pw}, nil
}

type h2StreamConn struct {
	ctx context.Context
	r   io.Reader
	w   io.WriteCloser
	mu  sync.Mutex
	buf []byte
}

func (c *h2StreamConn) Read(b []byte) (n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.buf) == 0 {
		var sz uint32
		if err := binary.Read(c.r, binary.BigEndian, &sz); err != nil {
			return 0, err
		}
		if sz == 0 {
			continue
		}
		if sz > 1<<20 {
			return 0, fmt.Errorf("grpc: frame too large")
		}
		c.buf = make([]byte, sz)
		if _, err := io.ReadFull(c.r, c.buf); err != nil {
			return 0, err
		}
	}
	n = copy(b, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *h2StreamConn) Write(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := binary.Write(c.w, binary.BigEndian, uint32(len(b))); err != nil {
		return 0, err
	}
	n, err = c.w.Write(b)
	return n, err
}

func (c *h2StreamConn) Close() error {
	if c.w != nil {
		return c.w.Close()
	}
	return nil
}
func (c *h2StreamConn) LocalAddr() net.Addr  { return &grpcAddr{} }
func (c *h2StreamConn) RemoteAddr() net.Addr { return &grpcAddr{} }
func (c *h2StreamConn) SetDeadline(t time.Time) error   { return nil }
func (c *h2StreamConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *h2StreamConn) SetWriteDeadline(t time.Time) error { return nil }

type grpcAddr struct{}
func (grpcAddr) Network() string { return "grpc" }
func (grpcAddr) String() string  { return "grpc" }

func (t *grpcTransport) Listen(addr multiaddr.Multiaddr) (net.Listener, error) {
	host, port, err := parseGRPCMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	addrStr := net.JoinHostPort(host, port)
	ln, err := net.Listen("tcp", addrStr)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	connCh := make(chan net.Conn, 1)
	mux.HandleFunc("/tunnel/stream", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Bidirectional: read from r.Body (length-prefixed), write to w (flush writer)
		w.Header().Set("Content-Type", "application/grpc")
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		flusher, _ := w.(http.Flusher)
		conn := &h2StreamServerConn{r: r.Body, w: w, flusher: flusher}
		select {
		case connCh <- conn:
		default:
			// conn.Close() no-op for server conn
		}
	})
	srv := &http.Server{Handler: h2c.NewHandler(mux, &http2.Server{})}
	go srv.Serve(ln)
	return &grpcListener{Listener: ln, connCh: connCh, srv: srv}, nil
}

type grpcListener struct {
	net.Listener
	connCh chan net.Conn
	srv    *http.Server
}

func (l *grpcListener) Accept() (net.Conn, error) {
	conn, ok := <-l.connCh
	if !ok {
		return nil, io.EOF
	}
	return conn, nil
}

type h2StreamServerConn struct {
	r      io.Reader
	w      io.Writer
	flusher http.Flusher
	mu      sync.Mutex
	buf     []byte
}

func (c *h2StreamServerConn) Read(b []byte) (n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.buf) == 0 {
		var sz uint32
		if err := binary.Read(c.r, binary.BigEndian, &sz); err != nil {
			return 0, err
		}
		if sz == 0 {
			continue
		}
		if sz > 1<<20 {
			return 0, fmt.Errorf("grpc: frame too large")
		}
		c.buf = make([]byte, sz)
		if _, err := io.ReadFull(c.r, c.buf); err != nil {
			return 0, err
		}
	}
	n = copy(b, c.buf)
	c.buf = c.buf[n:]
	return n, nil
}

func (c *h2StreamServerConn) Write(b []byte) (n int, err error) {
	if len(b) == 0 {
		return 0, nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := binary.Write(c.w, binary.BigEndian, uint32(len(b))); err != nil {
		return 0, err
	}
	n, err = c.w.Write(b)
	if err == nil && c.flusher != nil {
		c.flusher.Flush()
	}
	return n, err
}

func (c *h2StreamServerConn) Close() error { return nil }
func (c *h2StreamServerConn) LocalAddr() net.Addr  { return &grpcAddr{} }
func (c *h2StreamServerConn) RemoteAddr() net.Addr { return &grpcAddr{} }
func (c *h2StreamServerConn) SetDeadline(t time.Time) error   { return nil }
func (c *h2StreamServerConn) SetReadDeadline(t time.Time) error  { return nil }
func (c *h2StreamServerConn) SetWriteDeadline(t time.Time) error { return nil }

func parseGRPCMultiaddr(ma multiaddr.Multiaddr) (host, port string, err error) {
	if ma == nil {
		return "", "", fmt.Errorf("multiaddr is nil")
	}
	s := ma.String()
	parts := strings.Split(s, "/")
	for i := 1; i < len(parts); i++ {
		switch parts[i] {
		case "ip4", "ip6", "dns4", "dns6":
			if i+1 < len(parts) {
				host = parts[i+1]
				i++
			}
		case "tcp":
			if i+1 < len(parts) {
				port = parts[i+1]
				i++
			}
		}
	}
	if host == "" || port == "" {
		return "", "", fmt.Errorf("transport/grpc: invalid multiaddr %q", s)
	}
	return host, port, nil
}

func init() {
	DefaultRegistry.Register(GRPC())
}

