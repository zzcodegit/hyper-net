package obfuscate

import (
	"context"
	"crypto/tls"
	"net"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
)

// Браузерные ClientHelloID для случайного выбора (Chrome, Firefox, Safari/iOS).
var browserClientHelloIDs = []utls.ClientHelloID{
	utls.HelloChrome_Auto,
	utls.HelloFirefox_Auto,
	utls.HelloIOS_Auto,
}

var (
	muRandFingerprint sync.Mutex
	randFingerIndex   int
)

// RandomClientHelloID возвращает случайный fingerprint браузера (Chrome, Firefox, Safari) для каждого соединения.
func RandomClientHelloID() utls.ClientHelloID {
	muRandFingerprint.Lock()
	idx := randFingerIndex % len(browserClientHelloIDs)
	randFingerIndex++
	muRandFingerprint.Unlock()
	return browserClientHelloIDs[idx]
}

// DialUTLS устанавливает TLS-соединение с uTLS и случайным ClientHello (Chrome/Firefox/Safari).
// Используется для обфускации от анализа по TLS fingerprint.
// baseCfg может быть nil; при необходимости задайте serverName.
func DialUTLS(ctx context.Context, network, addr, serverName string, baseCfg *tls.Config) (net.Conn, error) {
	d := net.Dialer{}
	rawConn, err := d.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	uCfg := tlsConfigToUtls(baseCfg, serverName)
	uConn := utls.UClient(rawConn, uCfg, RandomClientHelloID())
	if err := uConn.HandshakeContext(ctx); err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	return uConn, nil
}

// tlsConfigToUtls копирует нужные для клиента поля из crypto/tls.Config в utls.Config.
func tlsConfigToUtls(c *tls.Config, serverName string) *utls.Config {
	uc := &utls.Config{}
	if c != nil {
		uc.RootCAs = c.RootCAs
		uc.InsecureSkipVerify = c.InsecureSkipVerify
		uc.NextProtos = c.NextProtos
		uc.ServerName = c.ServerName
	}
	if serverName != "" {
		uc.ServerName = serverName
	}
	return uc
}

// ListenUTLS создаёт TLS-листенер, для каждого принятого соединения выполняющий handshake
// с тем же набором параметров (серверная сторона остаётся стандартной crypto/tls;
// обфускация на клиенте при исходящих соединениях через DialUTLS).
func ListenUTLS(listener net.Listener, cfg *tls.Config) net.Listener {
	return tls.NewListener(listener, cfg)
}

func init() {
	// Инициализация индекса для чередования по времени, чтобы не всегда один и тот же порядок.
	randFingerIndex = int(time.Now().UnixNano() % int64(len(browserClientHelloIDs)))
}

