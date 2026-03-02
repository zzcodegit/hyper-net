package hysteria2

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sync"
	"time"

	"hypernet-node/hypernet/services/proxy/protoiface"
)

// ProtocolName — имя адаптера для Hysteria2‑подобного протокола.
// Полная реализация спецификации Hysteria2 выходит за рамки этого проекта,
// но адаптер реализует независимую зашифрованную фрейминговую сессию поверх
// существующего транспорта (TCP/QUIC), что делает трафик менее предсказуемым
// для DPI.
const ProtocolName = "hysteria2"

// Константы для простого AEAD‑фрейминга.
const (
	h2NonceSize   = 12                     // GCM nonce size
	h2HeaderSize  = 4 + h2NonceSize        // [payloadLen uint32][nonce 12b]
	h2MaxPayload  = 16 * 1024              // ограничиваем размер одного кадра
	h2MaxPadBytes = 512                    // верхняя граница случайного паддинга
)

type Hysteria2Protocol struct{}

func (p *Hysteria2Protocol) Name() string { return ProtocolName }

// Handshake инициализирует шифрованную сессию поверх предоставленного потока.
// В качестве ключа используется SHA‑256 от cfg.Token (если он пустой, ключ будет нулевым),
// что поверх уже зашифрованного libp2p‑транспорта даёт дополнительный слой
// обфускации и независимый фрейминг.
func (p *Hysteria2Protocol) Handshake(stream io.ReadWriteCloser, cfg *protoiface.Config) (protoiface.Session, error) {
	if cfg == nil {
		return nil, fmt.Errorf("hysteria2: missing config")
	}
	aead, err := newAEADFromToken(cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: %w", err)
	}
	return newSession(stream, aead), nil
}

// Wrap оборачивает существующее соединение net.Conn в ту же Hysteria2‑сессию.
func (p *Hysteria2Protocol) Wrap(conn net.Conn, cfg *protoiface.Config) (io.ReadWriteCloser, error) {
	if cfg == nil {
		return nil, fmt.Errorf("hysteria2: missing config")
	}
	aead, err := newAEADFromToken(cfg.Token)
	if err != nil {
		return nil, fmt.Errorf("hysteria2: %w", err)
	}
	return newSession(conn, aead), nil
}

func (p *Hysteria2Protocol) Features() protoiface.Features {
	return protoiface.Features{
		Transports:   []string{"quic", "tcp"},
		Mux:          true,
		Encryption:   "aes-256-gcm", // дополнительный шифровальный слой поверх транспорта
		Obfuscation:  true,
		Description:  "Hysteria2-like encrypted framing session over QUIC/TCP",
		Experimental: true,
	}
}

// hysteria2Session реализует io.ReadWriteCloser поверх базового потока с
// использованием AEAD (AES‑256‑GCM) и простого фрейминга:
//   [payloadLen uint32][nonce 12b][ciphertext]
// где payload может быть разбит на несколько кадров с небольшим случайным паддингом,
// чтобы размывать статистику размеров.
type hysteria2Session struct {
	base io.ReadWriteCloser
	aead cipher.AEAD

	mu      sync.Mutex // защищает closed
	closed  bool
	writeMu sync.Mutex
	readMu  sync.Mutex
	sendCtr uint64
}

func newAEADFromToken(token string) (cipher.AEAD, error) {
	sum := sha256.Sum256([]byte(token))
	block, err := aes.NewCipher(sum[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func newSession(base io.ReadWriteCloser, aead cipher.AEAD) *hysteria2Session {
	rand.Seed(time.Now().UnixNano())
	return &hysteria2Session{
		base: base,
		aead: aead,
	}
}

func (s *hysteria2Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	return s.base.Close()
}

func (s *hysteria2Session) Write(p []byte) (int, error) {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	s.mu.Unlock()

	written := 0
	for len(p) > 0 {
		chunk := p
		if len(chunk) > h2MaxPayload {
			chunk = chunk[:h2MaxPayload]
		}
		userLen := len(chunk)

		// Небольшой случайный паддинг внутри кадра.
		padLen := 0
		if h2MaxPadBytes > 0 {
			padLen = rand.Intn(h2MaxPadBytes + 1)
		}
		payload := make([]byte, 0, userLen+padLen)
		payload = append(payload, chunk...)
		if padLen > 0 {
			pad := make([]byte, padLen)
			if _, err := rand.Read(pad); err != nil {
				return written, err
			}
			payload = append(payload, pad...)
		}

		// nonce = счётчик кадра.
		nonce := make([]byte, h2NonceSize)
		binary.BigEndian.PutUint64(nonce[h2NonceSize-8:], s.sendCtr)
		s.sendCtr++

		ct := s.aead.Seal(nil, nonce, payload, nil)

		header := make([]byte, h2HeaderSize)
		binary.BigEndian.PutUint32(header[0:4], uint32(len(ct)))
		copy(header[4:], nonce)

		if _, err := s.base.Write(header); err != nil {
			return written, err
		}
		if _, err := s.base.Write(ct); err != nil {
			return written, err
		}

		written += userLen
		p = p[userLen:]
	}
	return written, nil
}

func (s *hysteria2Session) Read(p []byte) (int, error) {
	s.readMu.Lock()
	defer s.readMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return 0, io.EOF
	}
	s.mu.Unlock()

	// Читаем заголовок кадра.
	header := make([]byte, h2HeaderSize)
	if _, err := io.ReadFull(s.base, header); err != nil {
		return 0, err
	}
	ctLen := binary.BigEndian.Uint32(header[0:4])
	if ctLen == 0 {
		return 0, nil
	}
	nonce := header[4:]

	ct := make([]byte, ctLen)
	if _, err := io.ReadFull(s.base, ct); err != nil {
		return 0, err
	}

	plain, err := s.aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return 0, err
	}

	// plain содержит и полезные данные, и паддинг; вызывающий код видит только
	// первые len(p) байт, лишнее отбрасываем (обфускация не должна менять API).
	if len(plain) <= len(p) {
		copy(p, plain)
		return len(plain), nil
	}
	copy(p, plain[:len(p)])
	return len(p), nil
}

