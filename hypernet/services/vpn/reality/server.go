package reality

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"net"
	"strings"
	"time"

	reality "github.com/xtls/reality"
)

// ServerConfig задаёт параметры REALITY для сервера (маскировка под dest).
type ServerConfig struct {
	// Dest — реальный адрес для маскировки (например "www.microsoft.com:443" или "1.1.1.1:443").
	Dest string
	// ServerNames — допустимые SNI (из сертификата dest). Пустой или nil = разрешить любой.
	ServerNames []string
	// PrivateKey — X25519 приватный ключ в Base64 (как выдаёт xray x25519).
	PrivateKey string
	// ShortIds — список short ID в hex (каждый 2–16 hex-символов). Пустой = разрешить любой.
	ShortIds []string
	// Show — вывод отладочных сообщений в лог.
	Show bool
	// MaxTimeDiff — макс. допустимая разница времени с клиентом (0 = по умолчанию).
	MaxTimeDiff time.Duration
}

// ParseShortId превращает hex-строку (2–16 символов) в [8]byte для reality.Config.
func ParseShortId(s string) ([8]byte, bool) {
	s = strings.TrimSpace(s)
	if len(s) == 0 || len(s) > 16 || len(s)%2 != 0 {
		return [8]byte{}, false
	}
	raw, err := hex.DecodeString(s)
	if err != nil || len(raw) > 8 {
		return [8]byte{}, false
	}
	var out [8]byte
	copy(out[:], raw)
	return out, true
}

// BuildRealityConfig собирает *reality.Config из ServerConfig для вызова reality.Server.
func BuildRealityConfig(c *ServerConfig) (*reality.Config, error) {
	if c == nil || c.Dest == "" {
		return nil, fmt.Errorf("reality: Dest is required")
	}
	privRaw, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(c.PrivateKey))
	if err != nil {
		privRaw, err = base64.StdEncoding.DecodeString(strings.TrimSpace(c.PrivateKey))
	}
	if err != nil || len(privRaw) != 32 {
		return nil, fmt.Errorf("reality: PrivateKey must be 32 bytes Base64: %w", err)
	}

	cfg := &reality.Config{
		Show:        c.Show,
		Type:        "tcp",
		Dest:        c.Dest,
		Xver:        0,
		PrivateKey:  privRaw,
		MaxTimeDiff: c.MaxTimeDiff,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			d := net.Dialer{}
			return d.DialContext(ctx, network, address)
		},
	}
	cfg.ServerNames = make(map[string]bool)
	for _, n := range c.ServerNames {
		if s := strings.TrimSpace(n); s != "" {
			cfg.ServerNames[s] = true
		}
	}
	if len(cfg.ServerNames) == 0 {
		// Должен быть хотя бы один SNI для REALITY
		if idx := strings.Index(c.Dest, ":"); idx > 0 {
			cfg.ServerNames[c.Dest[:idx]] = true
		}
	}
	cfg.ShortIds = make(map[[8]byte]bool)
	for _, id := range c.ShortIds {
		if parsed, ok := ParseShortId(id); ok {
			cfg.ShortIds[parsed] = true
		}
	}
	return cfg, nil
}

// Listener оборачивает net.Listener и для каждого принятого соединения выполняет REALITY handshake.
type Listener struct {
	net.Listener
	config *reality.Config
	log    *log.Logger
}

// Accept возвращает соединение, обёрнутое в REALITY (после успешного handshake).
func (l *Listener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		realityConn, err := reality.Server(ctx, conn, l.config)
		cancel()
		if err != nil {
			if l.log != nil {
				l.log.Printf("reality handshake failed from %s: %v", conn.RemoteAddr(), err)
			}
			_ = conn.Close()
			continue
		}
		return realityConn, nil
	}
}

// ListenREALITY слушает на addr (например ":443") и для каждого входящего соединения
// выполняет REALITY Server handshake (маскировка под dest). При неверном клиенте
// трафик перенаправляется на dest (поведение библиотеки xtls/reality).
func ListenREALITY(addr string, cfg *ServerConfig) (net.Listener, error) {
	realityCfg, err := BuildRealityConfig(cfg)
	if err != nil {
		return nil, err
	}
	tcpLn, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("reality listen %s: %w", addr, err)
	}
	return &Listener{Listener: tcpLn, config: realityCfg, log: log.Default()}, nil
}

// SetLogger задаёт логгер для сообщений о неудачных handshake.
func (l *Listener) SetLogger(logger *log.Logger) {
	l.log = logger
}

var _ net.Listener = (*Listener)(nil)

// ClientConfig задаёт параметры для подключения к REALITY-серверу (клиент — в планах).
type ClientConfig struct {
	PublicKey   string // Base64 X25519 публичный ключ сервера
	ServerName  string // SNI (например из serverNames сервера)
	ShortId     string // hex short ID
	Fingerprint string // uTLS fingerprint: "chrome", "firefox", "ios" (пока не используется)
}

// DialREALITY подключается к REALITY-серверу. Реализация клиента — в планах;
// до этого можно использовать Xray-клиент с теми же publicKey/serverNames/shortId.
func DialREALITY(ctx context.Context, network, addr string, _ *ClientConfig) (net.Conn, error) {
	_ = ctx
	_ = network
	_ = addr
	return nil, fmt.Errorf("reality: client not implemented yet, use Xray client or wait for native support")
}

