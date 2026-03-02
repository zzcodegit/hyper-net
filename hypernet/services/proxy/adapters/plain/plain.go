package plain

import (
	"encoding/binary"
	"io"
	"math/rand"
	"net"
	"sync"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

// ProtocolName — имя базового адаптера без дополнительного шифрования/обфускации.
// Это минимальный «референсный» адаптер, который просто прокидывает байты.
// При включённой обфускации адаптер использует простую фрейминговую схему:
// каждый пользовательский Write превращается в кадр с заголовком
// [userLen(uint16)][padLen(uint16)] и payload+padding.
const ProtocolName = "plain"

// Ключи в Config.Raw для управления обфускацией.
const (
	obfsEnabledKey      = "obfs_enabled"           // bool, по умолчанию false
	obfsMaxPaddingKey   = "obfs_padding_max_bytes" // int, по умолчанию 0 (нет паддинга)
	obfsMaxJitterMsKey  = "obfs_jitter_max_ms"     // int, по умолчанию 0 (нет задержки)
	obfsDummyMinMsKey   = "obfs_dummy_min_ms"      // int, по умолчанию 0 (нет dummy-трафика)
	obfsDummyMaxMsKey   = "obfs_dummy_max_ms"      // int, по умолчанию 0 (нет dummy-трафика)
)

type PlainProtocol struct{}

func (p *PlainProtocol) Name() string { return ProtocolName }

// Handshake для plain-протокола по умолчанию просто возвращает исходный поток.
// Если в Config.Raw заданы параметры обфускации, возвращается обёртка,
// реализующая паддинг/джиттер/опциональный dummy-трафик.
func (p *PlainProtocol) Handshake(stream io.ReadWriteCloser, cfg *protoiface.Config) (protoiface.Session, error) {
	settings := parseObfsSettings(cfg)
	if !settings.enabled {
		return stream, nil
	}
	return newObfsSession(stream, settings), nil
}

// Wrap оборачивает существующее соединение net.Conn. Аналогично Handshake:
// при отключённой обфускации возвращает conn как есть, иначе — обёртку.
func (p *PlainProtocol) Wrap(conn net.Conn, cfg *protoiface.Config) (io.ReadWriteCloser, error) {
	settings := parseObfsSettings(cfg)
	if !settings.enabled {
		return conn, nil
	}
	return newObfsSession(conn, settings), nil
}

func (p *PlainProtocol) Features() protoiface.Features {
	return protoiface.Features{
		Transports:  []string{"tcp"},
		Mux:         false,
		Encryption:  "none", // шифрование обеспечивает сам libp2p/transport
		Obfuscation: true,
		Description: "plain adapter with optional padding/jitter obfuscation",
		// Явно помечаем как неэкспериментальный базовый вариант.
		Experimental: false,
	}
}

// DefaultConfig возвращает минимально полезную конфигурацию для plain-протокола.
// Обфускация по умолчанию включена с умеренными значениями по умолчанию;
// её можно явно отключить через Raw[obfs_enabled]=false.
func DefaultConfig() *protoiface.Config {
	return &protoiface.Config{
		Token:   "",
		Timeout: 5 * time.Second,
		Raw:     map[string]any{},
	}
}

// obfsSettings описывает параметры обфускации для сессии plain-протокола.
type obfsSettings struct {
	enabled       bool
	maxPadding    int
	maxJitter     time.Duration
	dummyMinDelay time.Duration
	dummyMaxDelay time.Duration
}

func parseObfsSettings(cfg *protoiface.Config) obfsSettings {
	// Значения по умолчанию: включённая обфускация с умеренным паддингом,
	// небольшим джиттером и редким dummy-трафиком.
	defaults := obfsSettings{
		enabled:       true,
		maxPadding:    64,
		maxJitter:     10 * time.Millisecond,
		dummyMinDelay: 500 * time.Millisecond,
		dummyMaxDelay: 1500 * time.Millisecond,
	}
	if cfg == nil || cfg.Raw == nil || len(cfg.Raw) == 0 {
		return defaults
	}
	raw := cfg.Raw

	// Явное отключение обфускации.
	if v, ok := raw[obfsEnabledKey].(bool); ok && !v {
		return obfsSettings{}
	}

	s := defaults

	if v, ok := raw[obfsMaxPaddingKey].(int); ok && v > 0 {
		s.maxPadding = v
	}
	if v, ok := raw[obfsMaxJitterMsKey].(int); ok && v > 0 {
		s.maxJitter = time.Duration(v) * time.Millisecond
	}
	var minDummy, maxDummy time.Duration
	if v, ok := raw[obfsDummyMinMsKey].(int); ok && v > 0 {
		minDummy = time.Duration(v) * time.Millisecond
	}
	if v, ok := raw[obfsDummyMaxMsKey].(int); ok {
		if v <= 0 {
			s.dummyMinDelay = 0
			s.dummyMaxDelay = 0
		} else {
			maxDummy = time.Duration(v) * time.Millisecond
		}
	}
	if maxDummy > 0 {
		if minDummy <= 0 || minDummy > maxDummy {
			minDummy = maxDummy / 2
		}
		s.dummyMinDelay = minDummy
		s.dummyMaxDelay = maxDummy
	}

	return s
}

// obfsSession реализует io.ReadWriteCloser поверх базового соединения, добавляя
// фрейминг, паддинг и опциональный dummy-трафик.
type obfsSession struct {
	base   io.ReadWriteCloser
	set    obfsSettings
	mu     sync.Mutex    // защищает буфер и закрытие
	wmu    sync.Mutex    // сериализует записи (включая dummy)
	closed bool

	// внутренний буфер для неполностью прочитанного пользовательского payload.
	pending []byte
}

func newObfsSession(base io.ReadWriteCloser, set obfsSettings) *obfsSession {
	s := &obfsSession{
		base: base,
		set:  set,
	}
	// Простая инициализация генератора случайностей для джиттера/паддинга.
	rand.Seed(time.Now().UnixNano())

	// Dummy-трафик запускаем только если явно задан интервал.
	if set.dummyMaxDelay > 0 {
		go s.runDummy()
	}
	return s
}

func (s *obfsSession) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.base.Close()
}

// Write кодирует пользовательские данные в один фрейм:
// [userLen(uint16)][padLen(uint16)] + payload + padding.
func (s *obfsSession) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.ErrClosedPipe
	}

	userLen := len(p)
	if userLen > 0xFFFF {
		userLen = 0xFFFF
		p = p[:userLen]
	}

	padLen := 0
	if s.set.maxPadding > 0 {
		padLen = rand.Intn(s.set.maxPadding + 1)
		if padLen > 0xFFFF {
			padLen = 0xFFFF
		}
	}

	// Небольшая случайная задержка перед отправкой.
	if s.set.maxJitter > 0 {
		d := time.Duration(rand.Int63n(int64(s.set.maxJitter)))
		if d > 0 {
			time.Sleep(d)
		}
	}

	header := make([]byte, 4)
	binary.BigEndian.PutUint16(header[0:2], uint16(userLen))
	binary.BigEndian.PutUint16(header[2:4], uint16(padLen))

	var pad []byte
	if padLen > 0 {
		pad = make([]byte, padLen)
		for i := range pad {
			pad[i] = byte(rand.Intn(256))
		}
	}

	frame := make([]byte, 0, 4+userLen+padLen)
	frame = append(frame, header...)
	frame = append(frame, p[:userLen]...)
	frame = append(frame, pad...)

	s.wmu.Lock()
	defer s.wmu.Unlock()
	n, err := s.base.Write(frame)
	if err != nil {
		return 0, err
	}
	// Сообщаем вызывающему только количество байт полезной нагрузки.
	if n < 4 {
		return 0, io.ErrShortWrite
	}
	return userLen, nil
}

// Read распаковывает фреймы и возвращает только пользовательскую часть.
// Dummy-фреймы (userLen == 0) отбрасываются и не видны вызывающему коду.
func (s *obfsSession) Read(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, io.EOF
	}

	// Сначала отдаём всё, что осталось от предыдущего payload.
	if len(s.pending) > 0 {
		n := copy(p, s.pending)
		s.pending = s.pending[n:]
		return n, nil
	}

	for {
		header := make([]byte, 4)
		if _, err := io.ReadFull(s.base, header); err != nil {
			return 0, err
		}
		userLen := int(binary.BigEndian.Uint16(header[0:2]))
		padLen := int(binary.BigEndian.Uint16(header[2:4]))

		total := userLen + padLen
		if total == 0 {
			// Пустой фрейм — пропускаем.
			continue
		}

		buf := make([]byte, total)
		if _, err := io.ReadFull(s.base, buf); err != nil {
			return 0, err
		}

		if userLen == 0 {
			// Dummy‑кадр: отбрасываем и читаем следующий.
			continue
		}

		if userLen <= len(p) {
			copy(p, buf[:userLen])
			return userLen, nil
		}

		// Пользовательский буфер меньше payload — часть кладём в pending.
		copy(p, buf[:len(p)])
		s.pending = append(s.pending, buf[len(p):userLen]...)
		return len(p), nil
	}
}

// runDummy периодически шлёт dummy‑кадры (userLen == 0, только паддинг),
// чтобы соединение не выглядело полностью простоявшим.
func (s *obfsSession) runDummy() {
	min := s.set.dummyMinDelay
	max := s.set.dummyMaxDelay
	if max <= 0 {
		return
	}
	for {
		delta := max - min
		sleep := min
		if delta > 0 {
			sleep += time.Duration(rand.Int63n(int64(delta)))
		}
		time.Sleep(sleep)

		s.mu.Lock()
		closed := s.closed
		s.mu.Unlock()
		if closed {
			return
		}

		// Отправляем dummy‑фрейм с нулевым userLen и небольшим паддингом.
		padLen := 1
		if s.set.maxPadding > 0 {
			padLen = 1 + rand.Intn(s.set.maxPadding)
			if padLen > 0xFFFF {
				padLen = 0xFFFF
			}
		}

		header := make([]byte, 4)
		// userLen == 0
		binary.BigEndian.PutUint16(header[2:4], uint16(padLen))

		pad := make([]byte, padLen)
		for i := range pad {
			pad[i] = byte(rand.Intn(256))
		}

		frame := make([]byte, 0, 4+padLen)
		frame = append(frame, header...)
		frame = append(frame, pad...)

		s.wmu.Lock()
		_, _ = s.base.Write(frame)
		s.wmu.Unlock()
	}
}

