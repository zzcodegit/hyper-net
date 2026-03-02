package proxy

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	metrics "hypernet-node/hypernet/services/proxy/metrics"
	"hypernet-node/pkg/protoadapters/hysteria2"
	obfuscate "hypernet-node/hypernet/core/network/obfuscate"
	"hypernet-node/pkg/protoadapters/shadowsocks"
	"hypernet-node/pkg/protoadapters/vless"
	"hypernet-node/pkg/protoadapters/vmess"
	"hypernet-node/hypernet/services/proxy/protoiface"
	ping "hypernet-node/hypernet/core/network/ping"

	"github.com/google/uuid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
	"github.com/oschwald/geoip2-golang"
)

const (
	// ProtocolID идентификатор прокси-протокола.
	ProtocolID = "/proxy/1.0.0"

	defaultDialTimeout = 10 * time.Second

	maxUDPPacketSize = 64 * 1024
)

// Request описывает запрос от клиента к прокси.
type Request struct {
	Proto string
	Host  string
	Port  int
}

var (
	errInvalidRequest = errors.New("invalid proxy request")
)

// prefixConn возвращает при чтении сначала prefix, затем читает из rest.
type prefixConn struct {
	prefix []byte
	off    int
	rest   io.ReadWriteCloser
}

func (p *prefixConn) Read(b []byte) (n int, err error) {
	if p.off < len(p.prefix) {
		n = copy(b, p.prefix[p.off:])
		p.off += n
		return n, nil
	}
	return p.rest.Read(b)
}

func (p *prefixConn) Write(b []byte) (n int, err error) { return p.rest.Write(b) }
func (p *prefixConn) Close() error                       { return p.rest.Close() }

// Server инкапсулирует состояние прокси для конкретной ноды:
// VLESS‑токен и кэш последнего согласованного протокола по пиру.
type Server struct {
	vlessToken string

	mu         sync.Mutex
	lastChosen map[peer.ID]string

	metrics         *metrics.Store
	host            host.Host
	qualityInterval time.Duration

	nodeRegion string
	geoIP      *geoip2.Reader

	// Обфускация: те же параметры, что и на клиенте (паддинг и задержка записи).
	paddingMin, paddingMax       int
	writeDelayMin, writeDelayMax time.Duration
}

// NewServer создаёт новый инстанс прокси-сервера без привязки к конкретному host.
func NewServer() *Server {
	return &Server{
		lastChosen: make(map[peer.ID]string),
	}
}

// SetVLESSAuthToken задаёт токен, который сервер ожидает от клиента при
// VLESS‑рукопожатии перед началом работы прокси.
func (s *Server) SetVLESSAuthToken(token string) {
	s.vlessToken = token
}

// SetMetricsStore подключает SQLite-хранилище для записи сессий.
func (s *Server) SetMetricsStore(m *metrics.Store) {
	s.metrics = m
}

// SetMetricsQualityInterval задаёт базовый интервал сбора quality‑метрик.
func (s *Server) SetMetricsQualityInterval(d time.Duration) {
	s.qualityInterval = d
}

// SetNodeRegion задаёт регион текущей ноды для записи в метриках.
func (s *Server) SetNodeRegion(region string) {
	s.nodeRegion = strings.TrimSpace(region)
}

// SetGeoIPReader подключает GeoIP2‑базу для определения client_region по IP.
func (s *Server) SetGeoIPReader(r *geoip2.Reader) {
	s.geoIP = r
}

// SetObfuscation задаёт параметры паддинга и задержки записи для handleProxyStream.
// Должны совпадать с параметрами клиента (PaddingMin/Max, WriteDelayMin/Max), иначе поток рассинхронизируется.
func (s *Server) SetObfuscation(paddingMin, paddingMax int, writeDelayMin, writeDelayMax time.Duration) {
	s.paddingMin, s.paddingMax = paddingMin, paddingMax
	s.writeDelayMin, s.writeDelayMax = writeDelayMin, writeDelayMax
}

// RememberChosenProtocol запоминает выбранный протокол для данного пира.
// Вызывается после успешного negotiation для этой ноды.
func (s *Server) RememberChosenProtocol(id peer.ID, protoName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if protoName == "" {
		delete(s.lastChosen, id)
		return
	}
	s.lastChosen[id] = protoName
}

// lastChosenProtocol возвращает последнее согласованное имя протокола для пира.
func (s *Server) lastChosenProtocol(id peer.ID) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastChosen[id]
}

// ParseRequest парсит первую строку запроса, ожидая формат:
//   TCP example.com 80
//   UDP 8.8.8.8 53
//   DIRECT example.com 443  — XTLS-режим: сырые байты без внутреннего шифрования (VLESS/VMess).
func ParseRequest(line string) (*Request, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, fmt.Errorf("%w: empty line", errInvalidRequest)
	}

	fields := strings.Fields(line)
	if len(fields) != 3 {
		return nil, fmt.Errorf("%w: expected 3 fields, got %d", errInvalidRequest, len(fields))
	}

	proto := strings.ToUpper(fields[0])
	if proto != "TCP" && proto != "UDP" && proto != "DIRECT" {
		return nil, fmt.Errorf("%w: unsupported proto %q", errInvalidRequest, proto)
	}

	host := fields[1]
	if host == "" {
		return nil, fmt.Errorf("%w: empty host", errInvalidRequest)
	}

	port, err := strconv.Atoi(fields[2])
	if err != nil || port <= 0 || port > 65535 {
		return nil, fmt.Errorf("%w: invalid port %q", errInvalidRequest, fields[2])
	}

	return &Request{
		Proto: proto,
		Host:  host,
		Port:  port,
	}, nil
}

// Register регистрирует обработчик прокси-протокола на хосте.
// Один инстанс Server должен использоваться совместно с соответствующим
// обработчиком negotiation-протокола той же ноды.
func (s *Server) Register(h host.Host) {
	s.host = h
	h.SetStreamHandler(ProtocolID, s.handleProxyStream)
}

func (s *Server) handleProxyStream(stream network.Stream) {
	defer stream.Close()

	ctx := context.Background()

	var sessionID string
	var counters *sessionCounters
	var success = true
	var errReason string
	chosenProto := s.lastChosenProtocol(stream.Conn().RemotePeer())
	if s.metrics != nil {
		clientRegion := ""
		if s.geoIP != nil {
			if ma := stream.Conn().RemoteMultiaddr(); ma != nil {
				var ipStr string
				if v, err := ma.ValueForProtocol(multiaddr.P_IP4); err == nil {
					ipStr = v
				} else if v6, err6 := ma.ValueForProtocol(multiaddr.P_IP6); err6 == nil {
					ipStr = v6
				}
				if ipStr != "" {
					if ip := net.ParseIP(ipStr); ip != nil {
						if city, err := s.geoIP.City(ip); err == nil && city != nil && city.Country.IsoCode != "" {
							clientRegion = strings.ToLower(city.Country.IsoCode)
						}
					}
				}
			}
		}

		sessionID = uuid.NewString()
		entryID := ""
		if s.host != nil {
			entryID = s.host.ID().String()
		}
		counters = &sessionCounters{}
		// Периодически снимаем quality‑метрики (RTT/jitter/loss) и динамику трафика.
		if s.qualityInterval > 0 {
			qi := s.qualityInterval
			if qi < 5*time.Second {
				qi = 5 * time.Second
			}
			done := make(chan struct{})
			defer close(done)
			go s.runQualityLoop(ctx, sessionID, stream.Conn().RemotePeer(), counters, qi, done)
		}
		s.metrics.StartSession(metrics.SessionStart{
			ID:            sessionID,
			EntryNodePeer: entryID,
			Protocol:      chosenProto,
			Transport:     string(stream.Protocol()),
			ClientRegion:  clientRegion,
			NodeRegion:    s.nodeRegion,
			StartedAt:     time.Now(),
		})
		defer func() {
			s.metrics.EndSession(metrics.SessionEnd{
				ID:          sessionID,
				BytesUp:     atomic.LoadInt64(&counters.up),
				BytesDown:   atomic.LoadInt64(&counters.down),
				Success:     success,
				ErrorReason: errReason,
				EndedAt:     time.Now(),
			})
		}()
	}

	// Если на ноде задан токен VLESS и для данного пира последним
	// согласован протокол "vless", пробуем выполнить VLESS-рукопожатие
	// перед тем, как читать строку запроса прокси. Это даёт простую
	// аутентификацию клиента по токену и не мешает fallback'у на другие
	// протоколы той же ноды.
	var rw io.ReadWriteCloser = stream
	var firstLine string
	var agreedObfs *ObfsParams

	// Согласование обфускации в начале потока /proxy/1.0.0: клиент может отправить
	// строку "OBFS <pmin> <pmax> <dmin> <dmax>\n"; сервер отвечает согласованными значениями.
	br := bufio.NewReader(rw)
	firstByte, err := br.ReadByte()
	if err == nil {
		line := string(firstByte)
		if firstByte == 'O' || firstByte == 'T' || firstByte == 'U' || firstByte == 'D' {
			rest, _ := br.ReadString('\n')
			line += rest
			if strings.HasPrefix(line, obfsPrefix) {
				if clientObfs, ok := ParseObfsLine(line); ok {
					agreed := s.AgreeObfs(clientObfs)
					if _, err := io.WriteString(rw, FormatObfsLine(agreed)); err != nil {
						return
					}
					agreedObfs = &agreed
					if agreed.PaddingMax > 0 {
						rw = obfuscate.NewPaddingConn(rw, agreed.PaddingMin, agreed.PaddingMax)
					}
					if agreed.WriteDelayMax > 0 {
						rw = obfuscate.NewDelayConn(rw, agreed.WriteDelayMin, agreed.WriteDelayMax)
					}
				}
			} else if strings.HasPrefix(line, "TCP ") || strings.HasPrefix(line, "UDP ") || strings.HasPrefix(line, "DIRECT ") {
				firstLine = line
			} else {
				rw = &prefixConn{prefix: []byte(line), rest: rw}
			}
		} else {
			// Не текстовая строка — возвращаем первый байт в поток для handshake.
			rw = &prefixConn{prefix: []byte{firstByte}, rest: rw}
		}
	}

	// VLESS‑рукопожатие (аутентификация по токену), если он задан и протокол выбран.
	// При legacy-запросе (firstLine уже содержит "TCP ..." или "UDP ...") рукопожатие не делаем.
	if firstLine == "" && s.vlessToken != "" && chosenProto == vless.ProtocolName {
		adapter := &vless.VLESSProtocol{}
		cfg := &protoiface.Config{
			Token:   s.vlessToken,
			Timeout: 5 * time.Second,
			Raw:     map[string]any{"role": "server"},
		}
		if _, err := adapter.Handshake(rw, cfg); err != nil {
			// В случае неуспешного рукопожатия логируем событие и закрываем поток.
			if s.metrics != nil && sessionID != "" {
				errReason = "vless_handshake_failed: " + err.Error()
				success = false
				s.metrics.AddEvent(sessionID, "handshake_failed", time.Now(), map[string]any{
					"protocol": "vless",
					"error":    err.Error(),
				})
			}
			return
		}
	} else if firstLine == "" && s.vlessToken != "" && chosenProto == vmess.ProtocolName {
		adapter := &vmess.VMESSProtocol{}
		cfg := &protoiface.Config{
			Token:   s.vlessToken,
			Timeout: 5 * time.Second,
			Raw:     map[string]any{"role": "server"},
		}
		if _, err := adapter.Handshake(rw, cfg); err != nil {
			if s.metrics != nil && sessionID != "" {
				errReason = "vmess_handshake_failed: " + err.Error()
				success = false
				s.metrics.AddEvent(sessionID, "handshake_failed", time.Now(), map[string]any{
					"protocol": "vmess",
					"error":    err.Error(),
				})
			}
			return
		}
	} else if firstLine == "" && s.vlessToken != "" && chosenProto == shadowsocks.ProtocolName {
		adapter := &shadowsocks.ShadowsocksProtocol{}
		cfg := &protoiface.Config{
			Token:   s.vlessToken,
			Timeout: 5 * time.Second,
			Raw:     map[string]any{"role": "server"},
		}
		sess, err := adapter.Handshake(rw, cfg)
		if err != nil {
			if s.metrics != nil && sessionID != "" {
				errReason = "shadowsocks_handshake_failed: " + err.Error()
				success = false
				s.metrics.AddEvent(sessionID, "handshake_failed", time.Now(), map[string]any{
					"protocol": "shadowsocks",
					"error":    err.Error(),
				})
			}
			return
		}
		rw = sess
	} else if firstLine == "" && chosenProto == hysteria2.ProtocolName {
		// Для Hysteria2 создаём зашифрованную фрейминговую сессию поверх базового потока.
		adapter := &hysteria2.Hysteria2Protocol{}
		cfg := &protoiface.Config{
			Token:   "", // при необходимости можно привязать к отдельному токену
			Timeout: 5 * time.Second,
			Raw:     map[string]any{"role": "server"},
		}
		sess, err := adapter.Handshake(rw, cfg)
		if err != nil {
			if s.metrics != nil && sessionID != "" {
				errReason = "hysteria2_handshake_failed: " + err.Error()
				success = false
				s.metrics.AddEvent(sessionID, "handshake_failed", time.Now(), map[string]any{
					"protocol": "hysteria2",
					"error":    err.Error(),
				})
			}
			return
		}
		rw = sess
	}

	// Обфускация: при согласовании (agreedObfs) обёртки уже применены; иначе — лимиты сервера из конфига.
	if agreedObfs == nil {
		if s.paddingMax > 0 {
			rw = obfuscate.NewPaddingConn(rw, s.paddingMin, s.paddingMax)
		}
		if s.writeDelayMax > 0 {
			rw = obfuscate.NewDelayConn(rw, s.writeDelayMin, s.writeDelayMax)
		}
	}

	reader := bufio.NewReader(rw)
	var line string
	if firstLine != "" {
		line = firstLine
	} else {
		line, err = reader.ReadString('\n')
	}
	if firstLine == "" && err != nil {
		// Не удалось прочитать запрос – логируем и закрываем поток.
		if s.metrics != nil && sessionID != "" {
			errReason = "read_request_failed: " + err.Error()
			success = false
			s.metrics.AddEvent(sessionID, "read_request_error", time.Now(), map[string]any{
				"error": err.Error(),
			})
		}
		return
	}

	req, err := ParseRequest(line)
	if err != nil {
		_, _ = io.WriteString(rw, "ERR "+err.Error()+"\n")
		if s.metrics != nil && sessionID != "" {
			errReason = "bad_request: " + err.Error()
			success = false
			s.metrics.AddEvent(sessionID, "bad_request", time.Now(), map[string]any{
				"line":  strings.TrimSpace(line),
				"error": err.Error(),
			})
		}
		return
	}

	switch strings.ToUpper(req.Proto) {
	case "TCP", "DIRECT":
		// DIRECT (XTLS): те же действия, что и TCP — подключение к цели и splice; данные после строки запроса идут без внутреннего шифрования.
		if err := handleTCPProxy(ctx, reader, rw, req, counters, s.metrics, sessionID); err != nil {
			if s.metrics != nil && sessionID != "" {
				errReason = "tcp_proxy_error: " + err.Error()
				success = false
				s.metrics.AddEvent(sessionID, "tcp_proxy_error", time.Now(), map[string]any{
					"host":  req.Host,
					"port":  req.Port,
					"error": err.Error(),
				})
			}
		}
	case "UDP":
		handleUDPProxy(ctx, reader, rw, req)
	default:
		_, _ = io.WriteString(rw, "ERR unsupported proto\n")
	}
}

type sessionCounters struct {
	up   int64
	down int64
}

// runQualityLoop периодически измеряет RTT до удалённого пира через ping‑протокол
// и записывает сэмплы качества в SQLite, одновременно отслеживая динамику трафика.
func (s *Server) runQualityLoop(parentCtx context.Context, sessionID string, remote peer.ID, ctrs *sessionCounters, interval time.Duration, done <-chan struct{}) {
	if s.metrics == nil || s.host == nil {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()

	ctx, cancel := context.WithCancel(parentCtx)
	defer cancel()

	var lastRTT time.Duration
	var sent, failed int64

	for {
		select {
		case <-done:
			return
		case <-ctx.Done():
			return
		case <-t.C:
			if sessionID == "" {
				continue
			}
			start := time.Now()

			// Измеряем RTT, открывая отдельный ping‑поток.
			var rtt time.Duration
			func() {
				cctx, ccancel := context.WithTimeout(ctx, 3*time.Second)
				defer ccancel()

				stream, err := s.host.NewStream(cctx, remote, ping.ProtocolID)
				if err != nil {
					failed++
					return
				}
				defer stream.Close()

				if _, err := io.WriteString(stream, "ping\n"); err != nil {
					failed++
					return
				}
				reader := bufio.NewReader(stream)
				if _, err := reader.ReadString('\n'); err != nil {
					failed++
					return
				}
				rtt = time.Since(start)
				sent++
			}()

			var rttMS, jitterMS int64
			var lossPct float64

			if rtt > 0 {
				rttMS = rtt.Milliseconds()
				if lastRTT > 0 {
					delta := rtt - lastRTT
					if delta < 0 {
						delta = -delta
					}
					jitterMS = delta.Milliseconds()
				}
				lastRTT = rtt
			}

			total := sent + failed
			if total > 0 {
				lossPct = float64(failed) / float64(total) * 100.0
			}

			s.metrics.AddQualitySample(sessionID, time.Now(), rttMS, jitterMS, lossPct)
		}
	}
}

func handleTCPProxy(ctx context.Context, reader *bufio.Reader, s io.ReadWriteCloser, req *Request, counters *sessionCounters, store *metrics.Store, sessionID string) error {
	targetConn, err := dialTCP(ctx, req.Host, req.Port)
	if err != nil {
		_, _ = io.WriteString(s, "ERR dial target: "+err.Error()+"\n")
		if store != nil && sessionID != "" {
			store.AddEvent(sessionID, "dial_error", time.Now(), map[string]any{
				"host":  req.Host,
				"port":  req.Port,
				"error": err.Error(),
			})
		}
		return fmt.Errorf("dial target: %w", err)
	}
	defer targetConn.Close()

	// Успешное рукопожатие: сообщаем клиенту, что соединение с целевым хостом установлено.
	if _, err := io.WriteString(s, "OK\n"); err != nil {
		if store != nil && sessionID != "" {
			store.AddEvent(sessionID, "write_ok_error", time.Now(), map[string]any{
				"error": err.Error(),
			})
		}
		return fmt.Errorf("write OK: %w", err)
	}

	// Двунаправленный proxy: stream <-> targetConn.
	pipeBidirectional(reader, s, targetConn, counters)

	// Нормальное завершение TCP‑прокси без явной ошибки.
	if store != nil && sessionID != "" {
		store.AddEvent(sessionID, "session_closed", time.Now(), map[string]any{
			"host": req.Host,
			"port": req.Port,
		})
	}
	return nil
}

func handleUDPProxy(ctx context.Context, reader *bufio.Reader, s io.ReadWriteCloser, req *Request) {
	conn, err := dialUDP(ctx, req.Host, req.Port)
	if err != nil {
		_, _ = io.WriteString(s, "ERR dial udp target: "+err.Error()+"\n")
		return
	}
	defer conn.Close()

	var wg sync.WaitGroup
	wg.Add(2)

	// stream -> UDP (читаем length-prefixed датаграммы)
	go func() {
		defer wg.Done()
		lenBuf := make([]byte, 2)
		for {
			if _, err := io.ReadFull(reader, lenBuf); err != nil {
				return
			}
			n := int(binary.BigEndian.Uint16(lenBuf))
			if n <= 0 || n > maxUDPPacketSize {
				continue
			}
			buf := make([]byte, n)
			if _, err := io.ReadFull(reader, buf); err != nil {
				return
			}
			if _, err := conn.Write(buf); err != nil {
				return
			}
		}
	}()

	// UDP -> stream (обратно датаграммы как length-prefixed)
	go func() {
		defer wg.Done()
		buf := make([]byte, maxUDPPacketSize)
		for {
			n, _, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n <= 0 {
				continue
			}
			lenBuf := make([]byte, 2)
			binary.BigEndian.PutUint16(lenBuf, uint16(n))
			if _, err := s.Write(lenBuf); err != nil {
				return
			}
			if _, err := s.Write(buf[:n]); err != nil {
				return
			}
		}
	}()

	wg.Wait()
}

// dialTCP инкапсулирует установку TCP-соединения с учётом таймаута.
func dialTCP(ctx context.Context, host string, port int) (net.Conn, error) {
	targetAddr := net.JoinHostPort(host, strconv.Itoa(port))
	dialCtx, cancel := context.WithTimeout(ctx, defaultDialTimeout)
	defer cancel()
	var d net.Dialer
	return d.DialContext(dialCtx, "tcp", targetAddr)
}

// dialUDP инкапсулирует установку UDP-соединения с учётом таймаута.
func dialUDP(ctx context.Context, host string, port int) (*net.UDPConn, error) {
	targetAddr := net.JoinHostPort(host, strconv.Itoa(port))
	raddr, err := net.ResolveUDPAddr("udp", targetAddr)
	if err != nil {
		return nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", raddr.String())
	if err != nil {
		return nil, err
	}
	uc, ok := conn.(*net.UDPConn)
	if !ok {
		_ = conn.Close()
		return nil, fmt.Errorf("expected *net.UDPConn, got %T", conn)
	}
	return uc, nil
}

// pipeBidirectional организует двунаправленную передачу данных между
// потоком (stream) и целевым TCP-соединением.
//
// upstreamReader уже содержит буферизированный reader над stream'ом, в котором
// может быть непрочитанный остаток после строки запроса.
func pipeBidirectional(upstreamReader *bufio.Reader, stream io.ReadWriteCloser, target net.Conn, counters *sessionCounters) {
	var wg sync.WaitGroup
	wg.Add(2)

	// stream -> target
	go func() {
		defer wg.Done()
		// Сначала читаем из buffered reader (учитывая уже считанную часть),
		// затем всё остальное напрямую из stream.
		if n, err := io.Copy(target, upstreamReader); err != nil {
			if counters != nil {
				atomic.AddInt64(&counters.up, n)
			}
			_ = target.Close()
			_ = stream.Close()
			return
		} else if counters != nil {
			atomic.AddInt64(&counters.up, n)
		}
	}()

	// target -> stream
	go func() {
		defer wg.Done()
		if n, err := io.Copy(stream, target); err != nil {
			if counters != nil {
				atomic.AddInt64(&counters.down, n)
			}
			_ = target.Close()
			_ = stream.Close()
			return
		} else if counters != nil {
			atomic.AddInt64(&counters.down, n)
		}
	}()

	wg.Wait()
}

