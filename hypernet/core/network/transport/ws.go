package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/multiformats/go-multiaddr"

	obfuscate "hypernet-node/hypernet/core/network/obfuscate"
)

const (
	WSTransportName  = "ws"
	WSSTransportName = "wss"
)

// useUTLSForWSS при true заставляет WSS использовать obfuscate.DialUTLS (браузерные TLS-отпечатки).
var useUTLSForWSS bool

// SetUseUTLSForWSS задаёт, использовать ли uTLS при Dial по wss. Вызывать из приложения при наличии Config.UseUTLS.
func SetUseUTLSForWSS(use bool) {
	useUTLSForWSS = use
}

type wsTransport struct {
	name   string
	secure bool // wss
}

// WS возвращает транспорт для WebSocket (multiaddr .../tcp/port/ws).
func WS() Transport { return &wsTransport{name: WSTransportName, secure: false} }

// WSS возвращает транспорт для WebSocket over TLS (multiaddr .../tcp/port/wss).
func WSS() Transport { return &wsTransport{name: WSSTransportName, secure: true} }

func (t *wsTransport) Name() string { return t.name }

func (t *wsTransport) Dial(ctx context.Context, addr multiaddr.Multiaddr) (net.Conn, error) {
	host, port, err := parseWSMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	addrStr := net.JoinHostPort(host, port)
	var rawConn net.Conn
	if t.secure {
		tlsCfg := &tls.Config{ServerName: host}
		var err error
		if useUTLSForWSS {
			rawConn, err = obfuscate.DialUTLS(ctx, "tcp", addrStr, host, tlsCfg)
		} else {
			d := tls.Dialer{Config: tlsCfg}
			rawConn, err = d.DialContext(ctx, "tcp", addrStr)
		}
		if err != nil {
			return nil, err
		}
	} else {
		d := net.Dialer{}
		var err error
		rawConn, err = d.DialContext(ctx, "tcp", addrStr)
		if err != nil {
			return nil, err
		}
	}
	scheme := "ws"
	if t.secure {
		scheme = "wss"
	}
	url := fmt.Sprintf("%s://%s/", scheme, addrStr)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		rawConn.Close()
		return nil, err
	}
	// Do not set Upgrade/Connection/Sec-* here — gorilla Dial adds them; duplicate causes handshake error.
	dialer := websocket.Dialer{NetDial: func(network, addr string) (net.Conn, error) { return rawConn, nil }}
	wsConn, _, err := dialer.Dial(url, req.Header)
	if err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("websocket handshake: %w", err)
	}
	return &wsConnWrap{Conn: wsConn}, nil
}

func (t *wsTransport) Listen(addr multiaddr.Multiaddr) (net.Listener, error) {
	host, port, err := parseWSMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	addrStr := net.JoinHostPort(host, port)
	ln, err := net.Listen("tcp", addrStr)
	if err != nil {
		return nil, err
	}
	upgrader := &websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return &wsListener{Listener: ln, upgrader: upgrader}, nil
}

func init() {
	DefaultRegistry.Register(WS())
	DefaultRegistry.Register(WSS())
}

// parseWSMultiaddr извлекает host и port из multiaddr с .../tcp/port/ws или .../tcp/port/wss.
func parseWSMultiaddr(ma multiaddr.Multiaddr) (host, port string, err error) {
	if ma == nil {
		return "", "", fmt.Errorf("multiaddr is nil")
	}
	s := ma.String()
	parts := strings.Split(s, "/")
	host = ""
	port = ""
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
		return "", "", fmt.Errorf("transport/ws: invalid multiaddr %q (need host and tcp/port)", s)
	}
	return host, port, nil
}

// wsConnWrap реализует net.Conn поверх *websocket.Conn (только бинарные кадры).
type wsConnWrap struct {
	*websocket.Conn
	mu     sync.Mutex
	readBuf []byte
}

func (c *wsConnWrap) Read(b []byte) (n int, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.readBuf) == 0 {
		_, p, err := c.Conn.ReadMessage()
		if err != nil {
			return 0, err
		}
		c.readBuf = p
	}
	n = copy(b, c.readBuf)
	c.readBuf = c.readBuf[n:]
	return n, nil
}

func (c *wsConnWrap) Write(b []byte) (n int, err error) {
	err = c.Conn.WriteMessage(websocket.BinaryMessage, b)
	if err != nil {
		return 0, err
	}
	return len(b), nil
}

func (c *wsConnWrap) SetDeadline(t time.Time) error {
	_ = c.Conn.SetReadDeadline(t)
	_ = c.Conn.SetWriteDeadline(t)
	return nil
}

// wsListener принимает TCP и при Accept выполняет WebSocket upgrade.
type wsListener struct {
	net.Listener
	upgrader *websocket.Upgrader
}

func (l *wsListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	req, err := http.ReadRequest(bufio.NewReader(conn))
	if err != nil {
		conn.Close()
		return nil, err
	}
	// Upgrader требует ResponseWriter и *Request. Создаём минимальный response.
	// Мы не можем использовать Upgrader.Upgrade с уже принятым conn напрямую —
	// Upgrade пишет ответ в ResponseWriter. Поэтому делаем свой upgrade: читаем
	// запрос уже выше, теперь пишем ответ и оборачиваем conn в websocket.
	// Проще: использовать hijack. Но Upgrader работает с http.ResponseWriter.
	// Альтернатива: серверный handshake вручную.
	// Используем httptest.ResponseRecorder? Нет — нужен реальный conn.
	// См. nhooyr.io/websocket — Accept(ctx, conn) принимает net.Conn.
	// У gorilla: Upgrader.Upgrade(w, r, nil) где w должен быть http.ResponseWriter
	// с поддержкой Hijack. Значит нам нужен fake ResponseWriter что Hijack возвращает conn.
	// Создаём type that implements ResponseWriter and Hijacker.
	rw := &hijackRW{conn: conn}
	wsConn, err := l.upgrader.Upgrade(rw, req, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &wsConnWrap{Conn: wsConn}, nil
}

// hijackRW реализует http.ResponseWriter и http.Hijacker для передачи conn в Upgrader.
type hijackRW struct {
	conn   net.Conn
	header http.Header
	code   int
}

func (h *hijackRW) Header() http.Header {
	if h.header == nil {
		h.header = make(http.Header)
	}
	return h.header
}

func (h *hijackRW) Write(p []byte) (int, error) { return h.conn.Write(p) }

func (h *hijackRW) WriteHeader(code int) { h.code = code }

func (h *hijackRW) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	rw := bufio.NewReadWriter(bufio.NewReader(h.conn), bufio.NewWriter(h.conn))
	return h.conn, rw, nil
}

