package vless

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

// ProtocolName — имя адаптера, соответствующего VLESS-подобному протоколу.
// Полная спецификация VLESS сложна, поэтому на первом этапе реализуем
// упрощённый вариант с аутентификацией токеном и прозрачной передачей данных.
const ProtocolName = "vless"

// cfgKeyRole используется в Config.Raw для указания роли ("client" или "server").
const cfgKeyRole = "role"

// roleClient / roleServer — допустимые значения роли.
const (
	roleClient = "client"
	roleServer = "server"
)

type VLESSProtocol struct{}

func (p *VLESSProtocol) Name() string { return ProtocolName }

// Handshake выполняет простое рукопожатие на основе pre-shared токена:
// - client: отправляет "<token>\n", ждёт "OK\n" от сервера;
// - server: читает строку, сравнивает с токеном и отвечает "OK\n" или "ERR\n".
// После успешного рукопожатия возвращается поток-сессия (пока без доп. шифрования).
func (p *VLESSProtocol) Handshake(rwc io.ReadWriteCloser, cfg *protoiface.Config) (protoiface.Session, error) {
	if cfg == nil {
		return nil, fmt.Errorf("vless: missing config")
	}
	roleAny, ok := cfg.Raw[cfgKeyRole]
	if !ok {
		return nil, fmt.Errorf("vless: missing role in config.Raw[%q]", cfgKeyRole)
	}
	role, ok := roleAny.(string)
	if !ok {
		return nil, fmt.Errorf("vless: role must be string")
	}

	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, fmt.Errorf("vless: empty token")
	}

	if cfg.Timeout > 0 {
		if c, ok := rwc.(interface{ SetDeadline(time.Time) error }); ok {
			_ = c.SetDeadline(time.Now().Add(cfg.Timeout))
		}
	}

	switch role {
	case roleClient:
		if err := p.clientHandshake(rwc, token); err != nil {
			return nil, err
		}
	case roleServer:
		if err := p.serverHandshake(rwc, token); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("vless: unknown role %q", role)
	}

	// На текущем этапе после успешного рукопожатия мы не меняем rwc,
	// а просто возвращаем его как Session. В следующих итерациях сюда
	// можно добавить шифрование/обфускацию поверх rwc.
	return rwc, nil
}

func (p *VLESSProtocol) clientHandshake(rwc io.ReadWriteCloser, token string) error {
	if _, err := io.WriteString(rwc, token+"\n"); err != nil {
		return fmt.Errorf("vless client: write token: %w", err)
	}
	br := bufio.NewReader(rwc)
	line, err := br.ReadString('\n')
	if err != nil {
		return fmt.Errorf("vless client: read response: %w", err)
	}
	line = strings.TrimSpace(line)
	if line != "OK" {
		return fmt.Errorf("vless client: unexpected response %q", line)
	}
	return nil
}

func (p *VLESSProtocol) serverHandshake(rwc io.ReadWriteCloser, token string) error {
	br := bufio.NewReader(rwc)
	line, err := br.ReadString('\n')
	if err != nil {
		_, _ = io.WriteString(rwc, "ERR\n")
		return fmt.Errorf("vless server: read token: %w", err)
	}
	line = strings.TrimSpace(line)
	if line != token {
		_, _ = io.WriteString(rwc, "ERR\n")
		return fmt.Errorf("vless server: invalid token")
	}
	if _, err := io.WriteString(rwc, "OK\n"); err != nil {
		return fmt.Errorf("vless server: write OK: %w", err)
	}
	return nil
}

// Wrap для первой версии просто возвращает исходный net.Conn.
// В дальнейшем сюда можно добавить полноценную VLESS-обёртку поверх TCP/TLS.
func (p *VLESSProtocol) Wrap(conn net.Conn, cfg *protoiface.Config) (io.ReadWriteCloser, error) {
	_ = cfg
	return conn, nil
}

func (p *VLESSProtocol) Features() protoiface.Features {
	return protoiface.Features{
		Transports:   []string{"tcp"},
		Mux:          false,
		Encryption:   "none", // шифрование может обеспечивать транспорт (TLS/QUIC)
		Obfuscation:  true,
		Description:  "minimal VLESS-like adapter with token-based handshake",
		Experimental: true,
	}
}

