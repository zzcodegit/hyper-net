package shadowsocks

import (
	"fmt"
	"io"
	"net"
	"strings"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

// ProtocolName — имя адаптера Shadowsocks (AEAD, AES-256-GCM).
const ProtocolName = "shadowsocks"

const cfgKeyRole = "role"
const roleClient = "client"
const roleServer = "server"

type ShadowsocksProtocol struct{}

func (p *ShadowsocksProtocol) Name() string { return ProtocolName }

// Handshake создаёт шифрованную сессию: ключ из пароля (Token), обмен данными в формате
// [nonce 12][length 2][ciphertext+tag]. Роль задаётся в cfg.Raw["role"] ("client" или "server").
func (p *ShadowsocksProtocol) Handshake(stream io.ReadWriteCloser, cfg *protoiface.Config) (protoiface.Session, error) {
	if cfg == nil {
		return nil, fmt.Errorf("shadowsocks: missing config")
	}
	password := strings.TrimSpace(cfg.Token)
	if password == "" {
		return nil, fmt.Errorf("shadowsocks: empty password/token")
	}
	roleAny, ok := cfg.Raw[cfgKeyRole]
	if !ok {
		return nil, fmt.Errorf("shadowsocks: missing role in config.Raw[%q]", cfgKeyRole)
	}
	role, ok := roleAny.(string)
	if !ok {
		return nil, fmt.Errorf("shadowsocks: role must be string")
	}
	if role != roleClient && role != roleServer {
		return nil, fmt.Errorf("shadowsocks: role must be %q or %q", roleClient, roleServer)
	}
	return newSSAEAD(stream, password)
}

// Wrap оборачивает net.Conn в шифрованную сессию (тот же формат, что и Handshake).
func (p *ShadowsocksProtocol) Wrap(conn net.Conn, cfg *protoiface.Config) (io.ReadWriteCloser, error) {
	if cfg == nil || strings.TrimSpace(cfg.Token) == "" {
		return conn, nil
	}
	return newSSAEAD(conn, strings.TrimSpace(cfg.Token))
}

func (p *ShadowsocksProtocol) Features() protoiface.Features {
	return protoiface.Features{
		Transports:   []string{"tcp"},
		Mux:          false,
		Encryption:   "AES-256-GCM (AEAD)",
		Obfuscation:  true,
		Description:  "Shadowsocks with AES-256-GCM, password-based key derivation",
		Experimental: false,
	}
}
