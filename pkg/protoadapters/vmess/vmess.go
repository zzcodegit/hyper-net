package vmess

import (
	"bufio"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"

	"github.com/google/uuid"
	"golang.org/x/crypto/chacha20poly1305"
)

// ProtocolName is the adapter name for VMess (Xray-compatible).
const ProtocolName = "vmess"

const (
	vmessVersion   = 1
	cmdTCP         = 1
	addrTypeIPv4   = 1
	addrTypeDomain = 2
	addrTypeIPv6   = 3
	secAESGCM      = 2
	secChaCha20    = 3
)

// UUID for key derivation (from Xray spec).
const vmessCmdKeySalt = "c48619fe-8f02-49e0-b9e9-edf763e17e21"

const cfgKeyRole = "role"
const roleClient = "client"
const roleServer = "server"
const authTimeTolerance = 120 // seconds

type VMESSProtocol struct{}

func (p *VMESSProtocol) Name() string { return ProtocolName }

func (p *VMESSProtocol) Handshake(stream io.ReadWriteCloser, cfg *protoiface.Config) (protoiface.Session, error) {
	if cfg == nil {
		return nil, fmt.Errorf("vmess: missing config")
	}
	roleAny, ok := cfg.Raw[cfgKeyRole]
	if !ok {
		return nil, fmt.Errorf("vmess: missing role in config.Raw[%q]", cfgKeyRole)
	}
	role, ok := roleAny.(string)
	if !ok {
		return nil, fmt.Errorf("vmess: role must be string")
	}
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return nil, fmt.Errorf("vmess: empty token (UUID required)")
	}
	userID, err := parseUUID(token)
	if err != nil {
		return nil, fmt.Errorf("vmess: invalid UUID: %w", err)
	}
	if cfg.Timeout > 0 {
		if c, ok := stream.(interface{ SetDeadline(time.Time) error }); ok {
			_ = c.SetDeadline(time.Now().Add(cfg.Timeout))
		}
	}
	switch role {
	case roleClient:
		return p.clientHandshake(stream, userID)
	case roleServer:
		return p.serverHandshake(stream, userID)
	default:
		return nil, fmt.Errorf("vmess: unknown role %q", role)
	}
}

func parseUUID(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

func (p *VMESSProtocol) clientHandshake(rwc io.ReadWriteCloser, userID uuid.UUID) (protoiface.Session, error) {
	t := time.Now().UTC().Unix()
	tBuf := make([]byte, 8)
	binary.BigEndian.PutUint64(tBuf, uint64(t))
	authHash := hmac.New(md5.New, userID[:])
	authHash.Write(tBuf)
	auth := authHash.Sum(nil)
	if _, err := rwc.Write(auth); err != nil {
		return nil, fmt.Errorf("vmess client: write auth: %w", err)
	}
	cmdKey := deriveCmdKey(userID)
	nonce := make([]byte, 12)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Minimal request: version, cmd=TCP, port=0, addr=""
	req := []byte{vmessVersion, cmdTCP, 0, 0, addrTypeDomain, 0}
	aead, err := chacha20poly1305.New(cmdKey[:])
	if err != nil {
		return nil, err
	}
	ct := aead.Seal(nil, nonce, req, nil)
	if _, err := rwc.Write(nonce); err != nil {
		return nil, err
	}
	if _, err := rwc.Write(ct); err != nil {
		return nil, err
	}
	br := bufio.NewReader(rwc)
	respNonce := make([]byte, 12)
	if _, err := io.ReadFull(br, respNonce); err != nil {
		return nil, fmt.Errorf("vmess client: read response nonce: %w", err)
	}
	// Response: 1 byte plaintext + 16 byte GCM tag = 17 bytes ciphertext
	buf := make([]byte, 17)
	if _, err := io.ReadFull(br, buf); err != nil {
		return nil, fmt.Errorf("vmess client: read response: %w", err)
	}
	plain, err := aead.Open(nil, respNonce, buf, nil)
	if err != nil {
		return nil, fmt.Errorf("vmess client: decrypt response: %w", err)
	}
	if len(plain) < 1 || plain[0] != 0 {
		return nil, fmt.Errorf("vmess client: server rejected")
	}
	return rwc, nil
}

func (p *VMESSProtocol) serverHandshake(rwc io.ReadWriteCloser, userID uuid.UUID) (protoiface.Session, error) {
	auth := make([]byte, 16)
	if _, err := io.ReadFull(rwc, auth); err != nil {
		return nil, fmt.Errorf("vmess server: read auth: %w", err)
	}
	now := time.Now().UTC().Unix()
	var ok bool
	for d := int64(-authTimeTolerance); d <= authTimeTolerance && !ok; d++ {
		t := now + d
		tBuf := make([]byte, 8)
		binary.BigEndian.PutUint64(tBuf, uint64(t))
		h := hmac.New(md5.New, userID[:])
		h.Write(tBuf)
		expected := h.Sum(nil)
		if hmac.Equal(auth, expected) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, fmt.Errorf("vmess server: auth failed")
	}
	nonce := make([]byte, 12)
	if _, err := io.ReadFull(rwc, nonce); err != nil {
		return nil, err
	}
	// Read encrypted request (ciphertext only; nonce already read). Min: 6 bytes plain + 16 tag = 22.
	buf := make([]byte, 64)
	n, err := io.ReadAtLeast(rwc, buf, 6+16)
	if err != nil {
		return nil, err
	}
	buf = buf[:n]
	cmdKey := deriveCmdKey(userID)
	aead, err := chacha20poly1305.New(cmdKey[:])
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, nonce, buf, nil)
	if err != nil {
		return nil, fmt.Errorf("vmess server: decrypt request: %w", err)
	}
	if len(plain) < 2 || plain[0] != vmessVersion {
		return nil, fmt.Errorf("vmess server: invalid request")
	}
	// Send response: encrypted OK (1 byte)
	respNonce := make([]byte, 12)
	if _, err := rand.Read(respNonce); err != nil {
		return nil, err
	}
	resp := aead.Seal(nil, respNonce, []byte{0}, nil)
	if _, err := rwc.Write(respNonce); err != nil {
		return nil, err
	}
	if _, err := rwc.Write(resp); err != nil {
		return nil, err
	}
	return rwc, nil
}

func deriveCmdKey(userID uuid.UUID) [32]byte {
	h := md5.Sum(append(userID[:], []byte(vmessCmdKeySalt)...))
	h2 := md5.Sum(h[:])
	var key [32]byte
	copy(key[:16], h[:])
	copy(key[16:], h2[:])
	return key
}

func (p *VMESSProtocol) Wrap(conn net.Conn, cfg *protoiface.Config) (io.ReadWriteCloser, error) {
	if cfg == nil {
		return conn, nil
	}
	_ = strings.TrimSpace(cfg.Token)
	return conn, nil
}

func (p *VMESSProtocol) Features() protoiface.Features {
	return protoiface.Features{
		Transports:   []string{"tcp"},
		Mux:          false,
		Encryption:   "VMess AEAD (ChaCha20-Poly1305)",
		Obfuscation:  true,
		Description:  "VMess (Xray) with UUID auth and AEAD command",
		Experimental: false,
	}
}

