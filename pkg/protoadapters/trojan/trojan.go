package trojan

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

// ProtocolName — имя адаптера, реализующего упрощённый Trojan‑подобный протокол.
// Полная спецификация Trojan включает TLS и шифрование; на первом этапе
// реализуем токен‑рукопожатие и прозрачную передачу трафика поверх TCP/TLS.
const ProtocolName = "trojan"

type TrojanProtocol struct{}

func (p *TrojanProtocol) Name() string { return ProtocolName }

// Handshake выполняет простое рукопожатие на основе pre-shared токена:
// - client: отправляет "<token>\n", ждёт "OK\n" от сервера;
// - server: читает строку, сравнивает с токеном и отвечает "OK\n" или "ERR\n".
// После успешного рукопожатия возвращается поток-сессия (пока без доп. шифрования).
func (p *TrojanProtocol) Handshake(rwc io.ReadWriteCloser, cfg *protoiface.Config) (protoiface.Session, error) {
	if cfg == nil {
		return nil, fmt.Errorf("trojan: missing config")
	}

	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, fmt.Errorf("trojan: empty token")
	}

	if cfg.Timeout > 0 {
		if c, ok := rwc.(interface{ SetDeadline(time.Time) error }); ok {
			_ = c.SetDeadline(time.Now().Add(cfg.Timeout))
		}
	}

	role := strings.ToLower(strings.TrimSpace(fmt.Sprint(cfg.Raw["role"])))
	switch role {
	case "client":
		if err := clientHandshake(rwc, token); err != nil {
			return nil, err
		}
	case "server":
		if err := serverHandshake(rwc, token); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("trojan: unknown role %q", role)
	}

	return rwc, nil
}

func clientHandshake(rwc io.ReadWriteCloser, token string) error {
	if _, err := io.WriteString(rwc, token+"\n"); err != nil {
		return fmt.Errorf("trojan client: write token: %w", err)
	}
	br := bufio.NewReader(rwc)
	line, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("trojan client: read response: %w", err)
	}
	line = strings.TrimSpace(line)
	if line != "OK" {
		return fmt.Errorf("trojan client: unexpected response %q", line)
	}
	return nil
}

func serverHandshake(rwc io.ReadWriteCloser, token string) error {
	br := bufio.NewReader(rwc)
	line, err := br.ReadString('\n')
	if err != nil {
		_, _ = io.WriteString(rwc, "ERR\n")
		return fmt.Errorf("trojan server: read token: %w", err)
	}
	line = strings.TrimSpace(line)
	if line != token {
		_, _ = io.WriteString(rwc, "ERR\n")
		return fmt.Errorf("trojan server: invalid token")
	}
	if _, err := io.WriteString(rwc, "OK\n"); err != nil {
		return fmt.Errorf("trojan server: write OK: %w", err)
	}
	return nil
}

// Wrap для первой версии просто возвращает исходный net.Conn.
// В дальнейшем сюда можно добавить полноценную Trojan‑обёртку поверх TLS.
func (p *TrojanProtocol) Wrap(conn net.Conn, cfg *protoiface.Config) (io.ReadWriteCloser, error) {
	_ = cfg
	return conn, nil
}

func (p *TrojanProtocol) Features() protoiface.Features {
	return protoiface.Features{
		Transports:   []string{"tcp", "tls"},
		Mux:          false,
		Encryption:   "none", // шифрование выполняется транспортом (TLS)
		Obfuscation:  true,
		Description:  "minimal Trojan-like adapter with token-based handshake",
		Experimental: true,
	}
}

