package client

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"hypernet-node/hypernet/services/dns"
	obfuscate "hypernet-node/hypernet/core/network/obfuscate"
	"hypernet-node/pkg/protoadapters/hysteria2"
	"hypernet-node/pkg/protoadapters/plain"
	"hypernet-node/pkg/protoadapters/shadowsocks"
	"hypernet-node/pkg/protoadapters/trojan"
	"hypernet-node/pkg/protoadapters/vless"
	"hypernet-node/pkg/protoadapters/vmess"
	"hypernet-node/hypernet/services/proxy/protoiface"
	"hypernet-node/hypernet/services/proxy/protomanager"
	"hypernet-node/hypernet/services/proxy/protonegotiate"
	proxy "hypernet-node/hypernet/services/proxy/server"
	transport "hypernet-node/hypernet/core/network/transport"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// ProxyClient предоставляет API для установления прокси-соединений через удалённую ноду.
type ProxyClient struct {
	H        host.Host
	Peer     peer.ID
	addrInfo *peer.AddrInfo
	mgr      *protomanager.Manager
	// базовый порядок предпочтения протоколов для попыток подключения
	preferred []string
	chosenProto       string // последний согласованный протокол (vless/plain/...)
	chosenTransport   string // последний согласованный транспорт (tcp/ws/...), если negotiation вернул
	// vlessToken — (опциональный) pre-shared токен для VLESS‑рукопожатия.
	vlessToken string
	// простейшая телеметрия качества подключения (на уровне установления туннеля)
	lastDialLatency time.Duration
	errorCount      int
	// qualityLatencyThreshold задаёт порог задержки установления туннеля,
	// при превышении которого DialTCPWithAutoQuality будет автоматически
	// инициировать повторные попытки с другим порядком протоколов.
	qualityLatencyThreshold time.Duration
	// maxErrorsBeforeFallback задаёт порог количества ошибок, после которого
	// DialTCPWithAutoQuality также переключится на альтернативные протоколы.
	maxErrorsBeforeFallback int

	// NameSystem — опционально: DHT DNS для разрешения имён (ResolveName, DialByName).
	NameSystem *dns.NameSystem

	// Obfuscation: паддинг [PaddingMin, PaddingMax] и задержка записи [WriteDelayMin, WriteDelayMax].
	PaddingMin, PaddingMax           int
	WriteDelayMin, WriteDelayMax    time.Duration
	UseUTLS                          bool
}

// NewProxyClient создаёт ProxyClient по multiaddr вида /ip4/..../tcp/4001/p2p/<peerID>.
func NewProxyClient(h host.Host, targetMultiaddr string, vlessToken string) (*ProxyClient, error) {
	maddr, err := multiaddr.NewMultiaddr(targetMultiaddr)
	if err != nil {
		return nil, fmt.Errorf("invalid target multiaddr: %w", err)
	}
	info, err := peer.AddrInfoFromP2pAddr(maddr)
	if err != nil {
		return nil, fmt.Errorf("parse peer info: %w", err)
	}
	// На данном этапе регистрируем несколько адаптеров, предпочитая vless,
	// затем trojan/shadowsocks/hysteria2 и plain как базовый fallback.
	// Для профильного режима бенчмарка можно ограничиться только plain,
	// установив переменную окружения HYPERNET_BENCH_PLAIN_ONLY=1.
	var mgr *protomanager.Manager
	preferred := []string{
		vless.ProtocolName,
		vmess.ProtocolName,
		trojan.ProtocolName,
		shadowsocks.ProtocolName,
		hysteria2.ProtocolName,
		plain.ProtocolName,
	}
	if os.Getenv("HYPERNET_BENCH_PLAIN_ONLY") == "1" {
		mgr = protomanager.NewManager([]string{
			plain.ProtocolName,
		})
		mgr.Register(&plain.PlainProtocol{})
		preferred = []string{plain.ProtocolName}
	} else {
		mgr = protomanager.NewManager(preferred)
		mgr.Register(&vless.VLESSProtocol{})
		mgr.Register(&vmess.VMESSProtocol{})
		mgr.Register(&trojan.TrojanProtocol{})
		mgr.Register(&shadowsocks.ShadowsocksProtocol{})
		mgr.Register(&hysteria2.Hysteria2Protocol{})
		mgr.Register(&plain.PlainProtocol{})
	}

	return &ProxyClient{
		H:        h,
		Peer:     info.ID,
		addrInfo: info,
		mgr:      mgr,
		preferred: preferred,
		chosenProto:             "",
		vlessToken:              vlessToken,
		qualityLatencyThreshold: 0,
		maxErrorsBeforeFallback: 0,
	}, nil
}

// SetNameSystem задаёт DHT NameSystem для разрешения имён (ResolveName, DialByName).
func (c *ProxyClient) SetNameSystem(ns *dns.NameSystem) {
	c.NameSystem = ns
}

type obfuscationSettings struct {
	PaddingMin, PaddingMax     int
	WriteDelayMin, WriteDelayMax time.Duration
	UseUTLS                    bool
}

func (c *ProxyClient) obfuscationConfig() obfuscationSettings {
	return obfuscationSettings{
		PaddingMin:       c.PaddingMin,
		PaddingMax:       c.PaddingMax,
		WriteDelayMin:    c.WriteDelayMin,
		WriteDelayMax:    c.WriteDelayMax,
		UseUTLS:          c.UseUTLS,
	}
}

// ResolveName разрешает DHT-имя в список multiaddr ноды. Требует заданный NameSystem (SetNameSystem).
func (c *ProxyClient) ResolveName(ctx context.Context, name string) ([]multiaddr.Multiaddr, error) {
	if c.NameSystem == nil {
		return nil, fmt.Errorf("client: name system not set, cannot resolve name")
	}
	return c.NameSystem.Resolve(ctx, name)
}

// DialByName разрешает имя через ResolveName, подключается к полученной ноде и открывает TCP-туннель до targetHost:targetPort.
func (c *ProxyClient) DialByName(ctx context.Context, name string, targetHost string, targetPort int) (io.ReadWriteCloser, error) {
	addrs, err := c.ResolveName(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("resolve name %q: %w", name, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("resolve name %q: no addresses", name)
	}
	pc, err := NewProxyClient(c.H, addrs[0].String(), c.vlessToken)
	if err != nil {
		return nil, fmt.Errorf("proxy client for resolved node: %w", err)
	}
	return pc.DialTCP(ctx, targetHost, targetPort)
}

// SetQualityPolicy задаёт политику автоматического переключения протокола
// в DialTCPWithAutoQuality. Если latencyThreshold <= 0 и maxErrors <= 0,
// автоматическое переключение отключается.
func (c *ProxyClient) SetQualityPolicy(latencyThreshold time.Duration, maxErrors int) {
	c.qualityLatencyThreshold = latencyThreshold
	if maxErrors < 0 {
		maxErrors = 0
	}
	c.maxErrorsBeforeFallback = maxErrors
}

// DialTCP открывает TCP‑соединение до host:port через удалённую ноду.
// Сначала выполняется согласование протокола (/hypernet/negotiate/1.0.0),
// затем открывается поток /proxy/1.0.0 (legacy-транспорт) и поверх него
// строится двунаправленный туннель.
func (c *ProxyClient) DialTCP(ctx context.Context, host string, port int) (io.ReadWriteCloser, error) {
	transport.SetUseUTLSForWSS(c.UseUTLS)
	start := time.Now()

	// Если у нас есть полная AddrInfo (из multiaddr), используем её, чтобы
	// гарантированно иметь адреса в peerstore. Иначе пробуем подключиться
	// только по PeerID (адреса должны быть уже известны через DHT/relay).
	if c.addrInfo != nil {
		if err := c.H.Connect(ctx, *c.addrInfo); err != nil {
			c.errorCount++
			return nil, fmt.Errorf("connect to proxy peer: %w", err)
		}
	} else {
		if err := c.H.Connect(ctx, peer.AddrInfo{ID: c.Peer}); err != nil {
			c.errorCount++
			return nil, fmt.Errorf("connect to proxy peer: %w", err)
		}
	}

	// Шаг 1: согласование протокола и транспорта.
	if c.mgr != nil {
		if chosen, transport, err := protonegotiate.ClientNegotiate(ctx, c.H, c.Peer, c.mgr); err == nil {
			c.chosenProto = chosen
			c.chosenTransport = transport
		} else {
			// Для совместимости продолжаем с legacy /proxy/1.0.0.
		}
	}

	baseStream, err := c.H.NewStream(ctx, c.Peer, proxy.ProtocolID)
	if err != nil {
		c.errorCount++
		return nil, fmt.Errorf("open proxy stream: %w", err)
	}
	var stream io.ReadWriteCloser = baseStream

	obfsCfg := c.obfuscationConfig()
	var agreedObfs *proxy.ObfsParams
	if obfsCfg.PaddingMax > 0 || obfsCfg.WriteDelayMax > 0 {
		clientObfs := proxy.ObfsParams{
			PaddingMin: obfsCfg.PaddingMin, PaddingMax: obfsCfg.PaddingMax,
			WriteDelayMin: obfsCfg.WriteDelayMin, WriteDelayMax: obfsCfg.WriteDelayMax,
		}
		if _, err := io.WriteString(stream, proxy.FormatObfsLine(clientObfs)); err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("write obfs line: %w", err)
		}
		peek := bufio.NewReader(stream)
		reply, err := peek.ReadString('\n')
		if err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("read obfs reply: %w", err)
		}
		if agreed, ok := proxy.ParseObfsLine(reply); ok {
			agreedObfs = &agreed
			if agreed.PaddingMax > 0 {
				stream = obfuscate.NewPaddingConn(stream, agreed.PaddingMin, agreed.PaddingMax)
			}
			if agreed.WriteDelayMax > 0 {
				stream = obfuscate.NewDelayConn(stream, agreed.WriteDelayMin, agreed.WriteDelayMax)
			}
		}
	}
	pMin, pMax := obfsCfg.PaddingMin, obfsCfg.PaddingMax
	dMin, dMax := obfsCfg.WriteDelayMin, obfsCfg.WriteDelayMax
	if agreedObfs != nil {
		pMin, pMax = agreedObfs.PaddingMin, agreedObfs.PaddingMax
		dMin, dMax = agreedObfs.WriteDelayMin, agreedObfs.WriteDelayMax
	}

	// Если negotiation выбрал vless или vmess и у нас есть токен, выполняем
	// рукопожатие поверх /proxy-потока до отправки запроса.
	if c.chosenProto == vless.ProtocolName && c.vlessToken != "" {
		adapter := &vless.VLESSProtocol{}
		transport := c.chosenTransport
		if transport == "" {
			transport = c.mgr.DefaultTransport()
		}
		cfg := &protoiface.Config{
			Token:            c.vlessToken,
			Timeout:          5 * time.Second,
			Transport:        transport,
			PaddingMin:       pMin,
			PaddingMax:       pMax,
			WriteDelayMin:    dMin,
			WriteDelayMax:    dMax,
			UseUTLS:          obfsCfg.UseUTLS,
			Raw:              map[string]any{"role": "client"},
		}
		if _, err := adapter.Handshake(stream, cfg); err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("vless handshake failed: %w", err)
		}
	} else if c.chosenProto == vmess.ProtocolName && c.vlessToken != "" {
		adapter := &vmess.VMESSProtocol{}
		transport := c.chosenTransport
		if transport == "" {
			transport = c.mgr.DefaultTransport()
		}
		cfg := &protoiface.Config{
			Token:            c.vlessToken,
			Timeout:          5 * time.Second,
			Transport:        transport,
			PaddingMin:       pMin,
			PaddingMax:       pMax,
			WriteDelayMin:    dMin,
			WriteDelayMax:    dMax,
			UseUTLS:          obfsCfg.UseUTLS,
			Raw:              map[string]any{"role": "client"},
		}
		sess, err := adapter.Handshake(stream, cfg)
		if err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("vmess handshake failed: %w", err)
		}
		stream = sess
	} else if c.chosenProto == shadowsocks.ProtocolName && c.vlessToken != "" {
		adapter := &shadowsocks.ShadowsocksProtocol{}
		cfg := &protoiface.Config{
			Token:   c.vlessToken,
			Timeout: 5 * time.Second,
			PaddingMin: pMin, PaddingMax: pMax,
			WriteDelayMin: dMin, WriteDelayMax: dMax,
			UseUTLS: obfsCfg.UseUTLS,
			Raw:     map[string]any{"role": "client"},
		}
		sess, err := adapter.Handshake(stream, cfg)
		if err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("shadowsocks handshake failed: %w", err)
		}
		stream = sess
	} else if c.chosenProto == hysteria2.ProtocolName {
		transport := c.chosenTransport
		if transport == "" {
			transport = c.mgr.DefaultTransport()
		}
		adapter := &hysteria2.Hysteria2Protocol{}
		cfg := &protoiface.Config{
			Token:            c.vlessToken,
			Timeout:          5 * time.Second,
			Transport:        transport,
			PaddingMin:       pMin,
			PaddingMax:       pMax,
			WriteDelayMin:    dMin,
			WriteDelayMax:    dMax,
			UseUTLS:          obfsCfg.UseUTLS,
			Raw:              map[string]any{"role": "client"},
		}
		sess, err := adapter.Handshake(stream, cfg)
		if err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("hysteria2 handshake failed: %w", err)
		}
		stream = sess
	}

	// Обфускация уже применена при согласовании OBFS; иначе — по конфигу клиента.
	if agreedObfs == nil && obfsCfg.PaddingMax > 0 {
		stream = obfuscate.NewPaddingConn(stream, obfsCfg.PaddingMin, obfsCfg.PaddingMax)
	}
	if agreedObfs == nil && obfsCfg.WriteDelayMax > 0 {
		stream = obfuscate.NewDelayConn(stream, obfsCfg.WriteDelayMin, obfsCfg.WriteDelayMax)
	}

	reqLine := fmt.Sprintf("TCP %s %d\n", host, port)
	if _, err := io.WriteString(stream, reqLine); err != nil {
		_ = stream.Close()
		c.errorCount++
		return nil, fmt.Errorf("write proxy request: %w", err)
	}

	peek := bufio.NewReader(stream)
	line, err := peek.ReadString('\n')
	if err != nil && err != io.EOF {
		_ = stream.Close()
		c.errorCount++
		return nil, fmt.Errorf("read proxy response: %w", err)
	}

	if strings.HasPrefix(line, "ERR ") {
		_ = stream.Close()
		c.errorCount++
		return nil, fmt.Errorf("proxy error: %s", strings.TrimSpace(strings.TrimPrefix(line, "ERR ")))
	}

	// Протокол рукопожатия:
	// - при успехе сервер сначала посылает "OK\n", затем начинает проксировать трафик;
	// - для обратной совместимости, если первая строка не "OK" и не "ERR", считаем,
	//   что это уже начало полезного ответа целевого сервера.
	if strings.HasPrefix(line, "OK") {
		// Успешное рукопожатие – просто продолжаем читать/писать напрямую в stream.
		c.lastDialLatency = time.Since(start)
		return stream, nil
	}

	// В противном случае считаем, что началась передача полезных данных.
	// Возвращаем обёртку, которая сначала выдаст уже прочитанную часть (если она не пуста),
	// а затем будет читать напрямую из stream.
	if line != "" {
		// Мы прочитали некоторую начальную строку (например, начало HTTP‑ответа),
		// положим её обратно в буфер.
		c.lastDialLatency = time.Since(start)
		return &bufferedStream{
			R: io.MultiReader(strings.NewReader(line), peek),
			W: stream,
			C: stream,
		}, nil
	}

	c.lastDialLatency = time.Since(start)
	return stream, nil
}

// DialTCPDirect открывает TCP в режиме XTLS (DIRECT): без внутреннего шифрования VLESS/VMess.
// Запрос "DIRECT host port\n" и далее сырые байты (например TLS) идут через туннель без двойного шифрования.
// Используйте для TLS-назначений (например порт 443), чтобы трафик выглядел как обычный TLS и снизить нагрузку.
func (c *ProxyClient) DialTCPDirect(ctx context.Context, host string, port int) (io.ReadWriteCloser, error) {
	transport.SetUseUTLSForWSS(c.UseUTLS)
	start := time.Now()

	if c.addrInfo != nil {
		if err := c.H.Connect(ctx, *c.addrInfo); err != nil {
			c.errorCount++
			return nil, fmt.Errorf("connect to proxy peer: %w", err)
		}
	} else {
		if err := c.H.Connect(ctx, peer.AddrInfo{ID: c.Peer}); err != nil {
			c.errorCount++
			return nil, fmt.Errorf("connect to proxy peer: %w", err)
		}
	}

	baseStream, err := c.H.NewStream(ctx, c.Peer, proxy.ProtocolID)
	if err != nil {
		c.errorCount++
		return nil, fmt.Errorf("open proxy stream: %w", err)
	}
	var stream io.ReadWriteCloser = baseStream

	obfsCfg := c.obfuscationConfig()
	var agreedObfs *proxy.ObfsParams
	if obfsCfg.PaddingMax > 0 || obfsCfg.WriteDelayMax > 0 {
		clientObfs := proxy.ObfsParams{
			PaddingMin: obfsCfg.PaddingMin, PaddingMax: obfsCfg.PaddingMax,
			WriteDelayMin: obfsCfg.WriteDelayMin, WriteDelayMax: obfsCfg.WriteDelayMax,
		}
		if _, err := io.WriteString(stream, proxy.FormatObfsLine(clientObfs)); err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("write obfs line: %w", err)
		}
		peek := bufio.NewReader(stream)
		reply, err := peek.ReadString('\n')
		if err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("read obfs reply: %w", err)
		}
		if agreed, ok := proxy.ParseObfsLine(reply); ok {
			agreedObfs = &agreed
			if agreed.PaddingMax > 0 {
				stream = obfuscate.NewPaddingConn(stream, agreed.PaddingMin, agreed.PaddingMax)
			}
			if agreed.WriteDelayMax > 0 {
				stream = obfuscate.NewDelayConn(stream, agreed.WriteDelayMin, agreed.WriteDelayMax)
			}
		}
	}
	if agreedObfs == nil && obfsCfg.PaddingMax > 0 {
		stream = obfuscate.NewPaddingConn(stream, obfsCfg.PaddingMin, obfsCfg.PaddingMax)
	}
	if agreedObfs == nil && obfsCfg.WriteDelayMax > 0 {
		stream = obfuscate.NewDelayConn(stream, obfsCfg.WriteDelayMin, obfsCfg.WriteDelayMax)
	}

	reqLine := fmt.Sprintf("DIRECT %s %d\n", host, port)
	if _, err := io.WriteString(stream, reqLine); err != nil {
		_ = stream.Close()
		c.errorCount++
		return nil, fmt.Errorf("write DIRECT request: %w", err)
	}

	peek := bufio.NewReader(stream)
	line, err := peek.ReadString('\n')
	if err != nil && err != io.EOF {
		_ = stream.Close()
		c.errorCount++
		return nil, fmt.Errorf("read proxy response: %w", err)
	}
	if strings.HasPrefix(line, "ERR ") {
		_ = stream.Close()
		c.errorCount++
		return nil, fmt.Errorf("proxy error: %s", strings.TrimSpace(strings.TrimPrefix(line, "ERR ")))
	}
	if strings.HasPrefix(line, "OK") {
		c.lastDialLatency = time.Since(start)
		return stream, nil
	}
	if line != "" {
		c.lastDialLatency = time.Since(start)
		return &bufferedStream{
			R: io.MultiReader(strings.NewReader(line), peek),
			W: stream,
			C: stream,
		}, nil
	}
	c.lastDialLatency = time.Since(start)
	return stream, nil
}

// DialTCPWithFallback пытается установить TCP‑туннель, при необходимости
// переигрывая порядок предпочтения протоколов. Если первая попытка даёт
// слишком большую задержку или ошибку, порядок preferred поворачивается,
// и выполняются повторные попытки (до числа зарегистрированных протоколов).
func (c *ProxyClient) DialTCPWithFallback(ctx context.Context, host string, port int, latencyThreshold time.Duration) (io.ReadWriteCloser, error) {
	if len(c.preferred) == 0 {
		return c.DialTCP(ctx, host, port)
	}

	var lastErr error
	for attempt := 0; attempt < len(c.preferred); attempt++ {
		// Первая попытка использует текущий порядок, остальные — повёрнутый.
		if attempt > 0 {
			rotated := make([]string, len(c.preferred))
			copy(rotated, c.preferred[attempt:])
			copy(rotated[len(c.preferred)-attempt:], c.preferred[:attempt])

			mgr := protomanager.NewManager(rotated)
			mgr.Register(&vless.VLESSProtocol{})
			mgr.Register(&trojan.TrojanProtocol{})
			mgr.Register(&shadowsocks.ShadowsocksProtocol{})
			mgr.Register(&hysteria2.Hysteria2Protocol{})
			mgr.Register(&plain.PlainProtocol{})

			c.mgr = mgr
			c.chosenProto = ""
		}

		stream, err := c.DialTCP(ctx, host, port)
		if err != nil {
			lastErr = err
			continue
		}

		lat, _ := c.Stats()
		if latencyThreshold <= 0 || lat <= latencyThreshold {
			return stream, nil
		}

		// Слишком медленно: закрываем поток и пробуем другой приоритет.
		_ = stream.Close()
		lastErr = fmt.Errorf("dial latency %s exceeded threshold %s", lat, latencyThreshold)
	}

	if lastErr == nil {
		lastErr = fmt.Errorf("all dial attempts failed")
	}
	return nil, lastErr
}

// DialTCPWithAutoQuality использует накопленные метрики качества
// (latency/ошибки) и конфигурацию SetQualityPolicy, чтобы автоматически
// решать, когда переключаться на альтернативные протоколы. Это позволяет
// вызывать один метод вместо ручного цикла с DialTCP/DialTCPWithFallback.
func (c *ProxyClient) DialTCPWithAutoQuality(ctx context.Context, host string, port int) (io.ReadWriteCloser, error) {
	latThr := c.qualityLatencyThreshold
	maxErr := c.maxErrorsBeforeFallback

	// Если политика качества не задана, ведём себя как обычный DialTCP.
	if latThr <= 0 && maxErr <= 0 {
		return c.DialTCP(ctx, host, port)
	}

	// Если накопленное количество ошибок превышает порог, принудительно
	// запускаем fallback сразу, иначе даём шанс текущему порядку протоколов.
	if maxErr > 0 && c.errorCount >= maxErr {
		return c.DialTCPWithFallback(ctx, host, port, latThr)
	}

	// Сначала пробуем обычный DialTCP; если latency окажется выше порога,
	// DialTCPWithFallback внутри себя переиспользует Stats() и перезапустит
	// попытки с другим порядком протоколов.
	stream, err := c.DialTCP(ctx, host, port)
	if err != nil {
		// Ошибка при первичном подключении — пробуем полноценный fallback.
		return c.DialTCPWithFallback(ctx, host, port, latThr)
	}

	lat, _ := c.Stats()
	if latThr > 0 && lat > latThr {
		_ = stream.Close()
		return c.DialTCPWithFallback(ctx, host, port, latThr)
	}

	return stream, nil
}

// DialUDP открывает UDP‑туннель до host:port через удалённую ноду.
// Формат трафика: каждая UDP‑датаграмма инкапсулируется как
//   [2 байта big-endian длины][payload]
// и передаётся по libp2p‑потоку. Read/Write возвращают полные датаграммы.
func (c *ProxyClient) DialUDP(ctx context.Context, host string, port int) (io.ReadWriteCloser, error) {
	transport.SetUseUTLSForWSS(c.UseUTLS)
	start := time.Now()
	if c.addrInfo != nil {
		if err := c.H.Connect(ctx, *c.addrInfo); err != nil {
			c.errorCount++
			return nil, fmt.Errorf("connect to proxy peer: %w", err)
		}
	} else {
		if err := c.H.Connect(ctx, peer.AddrInfo{ID: c.Peer}); err != nil {
			c.errorCount++
			return nil, fmt.Errorf("connect to proxy peer: %w", err)
		}
	}

	if c.mgr != nil {
		if chosen, transport, err := protonegotiate.ClientNegotiate(ctx, c.H, c.Peer, c.mgr); err == nil {
			c.chosenProto = chosen
			c.chosenTransport = transport
		}
	}
	s, err := c.H.NewStream(ctx, c.Peer, proxy.ProtocolID)
	if err != nil {
		c.errorCount++
		return nil, fmt.Errorf("open proxy stream: %w", err)
	}
	var stream io.ReadWriteCloser = s

	obfsCfg := c.obfuscationConfig()
	var agreedObfs *proxy.ObfsParams
	if obfsCfg.PaddingMax > 0 || obfsCfg.WriteDelayMax > 0 {
		clientObfs := proxy.ObfsParams{
			PaddingMin: obfsCfg.PaddingMin, PaddingMax: obfsCfg.PaddingMax,
			WriteDelayMin: obfsCfg.WriteDelayMin, WriteDelayMax: obfsCfg.WriteDelayMax,
		}
		if _, err := io.WriteString(stream, proxy.FormatObfsLine(clientObfs)); err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("write obfs line: %w", err)
		}
		peek := bufio.NewReader(stream)
		reply, err := peek.ReadString('\n')
		if err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("read obfs reply: %w", err)
		}
		if agreed, ok := proxy.ParseObfsLine(reply); ok {
			agreedObfs = &agreed
			if agreed.PaddingMax > 0 {
				stream = obfuscate.NewPaddingConn(stream, agreed.PaddingMin, agreed.PaddingMax)
			}
			if agreed.WriteDelayMax > 0 {
				stream = obfuscate.NewDelayConn(stream, agreed.WriteDelayMin, agreed.WriteDelayMax)
			}
		}
	}
	pMin, pMax := obfsCfg.PaddingMin, obfsCfg.PaddingMax
	dMin, dMax := obfsCfg.WriteDelayMin, obfsCfg.WriteDelayMax
	if agreedObfs != nil {
		pMin, pMax = agreedObfs.PaddingMin, agreedObfs.PaddingMax
		dMin, dMax = agreedObfs.WriteDelayMin, agreedObfs.WriteDelayMax
	}

	// Аналогично TCP: если выбран vless и есть токен, выполняем рукопожатие
	// перед отправкой UDP-запроса.
	if c.chosenProto == vless.ProtocolName && c.vlessToken != "" {
		transport := c.chosenTransport
		if transport == "" {
			transport = c.mgr.DefaultTransport()
		}
		adapter := &vless.VLESSProtocol{}
		cfg := &protoiface.Config{
			Token:            c.vlessToken,
			Timeout:          5 * time.Second,
			Transport:        transport,
			PaddingMin:       pMin,
			PaddingMax:       pMax,
			WriteDelayMin:    dMin,
			WriteDelayMax:    dMax,
			UseUTLS:          obfsCfg.UseUTLS,
			Raw:              map[string]any{"role": "client"},
		}
		if _, err := adapter.Handshake(stream, cfg); err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("vless handshake failed: %w", err)
		}
	} else if c.chosenProto == vmess.ProtocolName && c.vlessToken != "" {
		transport := c.chosenTransport
		if transport == "" {
			transport = c.mgr.DefaultTransport()
		}
		adapter := &vmess.VMESSProtocol{}
		cfg := &protoiface.Config{
			Token:            c.vlessToken,
			Timeout:          5 * time.Second,
			Transport:        transport,
			PaddingMin:       pMin,
			PaddingMax:       pMax,
			WriteDelayMin:    dMin,
			WriteDelayMax:    dMax,
			UseUTLS:          obfsCfg.UseUTLS,
			Raw:              map[string]any{"role": "client"},
		}
		sess, err := adapter.Handshake(stream, cfg)
		if err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("vmess handshake failed: %w", err)
		}
		stream = sess
	} else if c.chosenProto == shadowsocks.ProtocolName && c.vlessToken != "" {
		adapter := &shadowsocks.ShadowsocksProtocol{}
		cfg := &protoiface.Config{
			Token:   c.vlessToken,
			Timeout: 5 * time.Second,
			PaddingMin: pMin, PaddingMax: pMax,
			WriteDelayMin: dMin, WriteDelayMax: dMax,
			UseUTLS: obfsCfg.UseUTLS,
			Raw:     map[string]any{"role": "client"},
		}
		sess, err := adapter.Handshake(stream, cfg)
		if err != nil {
			_ = stream.Close()
			c.errorCount++
			return nil, fmt.Errorf("shadowsocks handshake failed: %w", err)
		}
		stream = sess
	}

	if agreedObfs == nil && obfsCfg.PaddingMax > 0 {
		stream = obfuscate.NewPaddingConn(stream, obfsCfg.PaddingMin, obfsCfg.PaddingMax)
	}
	if agreedObfs == nil && obfsCfg.WriteDelayMax > 0 {
		stream = obfuscate.NewDelayConn(stream, obfsCfg.WriteDelayMin, obfsCfg.WriteDelayMax)
	}

	reqLine := fmt.Sprintf("UDP %s %d\n", host, port)
	if _, err := io.WriteString(stream, reqLine); err != nil {
		_ = stream.Close()
		c.errorCount++
		return nil, fmt.Errorf("write proxy request: %w", err)
	}

	c.lastDialLatency = time.Since(start)
	return &udpTunnel{
		stream: stream,
		r:      bufio.NewReader(stream),
	}, nil
}

type udpTunnel struct {
	stream io.ReadWriteCloser
	r      *bufio.Reader
}

func (u *udpTunnel) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(p) > 0xFFFF {
		p = p[:0xFFFF]
	}
	lenBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(lenBuf, uint16(len(p)))
	if _, err := u.stream.Write(lenBuf); err != nil {
		return 0, err
	}
	if _, err := u.stream.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (u *udpTunnel) Read(p []byte) (int, error) {
	lenBuf := make([]byte, 2)
	if _, err := io.ReadFull(u.r, lenBuf); err != nil {
		return 0, err
	}
	n := int(binary.BigEndian.Uint16(lenBuf))
	if n <= 0 {
		return 0, nil
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(u.r, buf); err != nil {
		return 0, err
	}
	if len(p) < n {
		copy(p, buf[:len(p)])
		return len(p), io.ErrShortBuffer
	}
	copy(p, buf)
	return n, nil
}

func (u *udpTunnel) Close() error {
	return u.stream.Close()
}

// Stats возвращает простые метрики качества последнего соединения.
func (c *ProxyClient) Stats() (lastDialLatency time.Duration, errorCount int) {
	return c.lastDialLatency, c.errorCount
}

// ChosenProtocol возвращает имя последнего согласованного протокола
// (например, "vless" или "plain") для отладки и тестов.
func (c *ProxyClient) ChosenProtocol() string {
	return c.chosenProto
}

// bufferedStream реализует io.ReadWriteCloser, используя отдельные Reader/Writer.
type bufferedStream struct {
	R io.Reader
	W io.Writer
	C io.Closer
}

func (b *bufferedStream) Read(p []byte) (int, error)  { return b.R.Read(p) }
func (b *bufferedStream) Write(p []byte) (int, error) { return b.W.Write(p) }
func (b *bufferedStream) Close() error                { return b.C.Close() }

