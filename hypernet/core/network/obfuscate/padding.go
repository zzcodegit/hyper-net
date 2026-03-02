package obfuscate

import (
	"encoding/binary"
	"io"
	"math/rand"
	"sync"
)

// PaddingConn оборачивает io.ReadWriteCloser и добавляет к каждому Write случайное
// количество мусорных байт (в диапазоне [PaddingMin, PaddingMax]). Формат кадра:
// [2 байта BE длина payload][payload][2 байта BE длина padding][padding].
// Длина padding в кадре — до MaxPaddingBytes (512) байт.
// Читающая сторона должна использовать такую же обёртку.
type PaddingConn struct {
	io.ReadWriteCloser
	PaddingMin, PaddingMax int
	rng                    *rand.Rand
	mu                     sync.Mutex
	readBuf                []byte
	readOff                int
}

// NewPaddingConn создаёт обёртку с настраиваемым паддингом. Если max <= 0, паддинг отключён (pass-through).
func NewPaddingConn(rwc io.ReadWriteCloser, paddingMin, paddingMax int) *PaddingConn {
	if paddingMax <= 0 {
		paddingMax = 0
		paddingMin = 0
	}
	if paddingMin > paddingMax {
		paddingMin = paddingMax
	}
	return &PaddingConn{
		ReadWriteCloser: rwc,
		PaddingMin:      paddingMin,
		PaddingMax:      paddingMax,
		rng:             rand.New(rand.NewSource(rand.Int63())),
	}
}

// Write отправляет данные в формате: 2 байта (длина p) + p + 2 байта (длина pad) + pad.
func (c *PaddingConn) Write(p []byte) (n int, err error) {
	padLen := c.nextPaddingLen()

	if c.PaddingMax <= 0 {
		_, err = c.ReadWriteCloser.Write(p)
		return len(p), err
	}
	if len(p) > 0xffff {
		_, err = c.ReadWriteCloser.Write(p)
		return len(p), err
	}
	buf := make([]byte, 2+len(p)+2+padLen)
	binary.BigEndian.PutUint16(buf[0:2], uint16(len(p)))
	copy(buf[2:], p)
	binary.BigEndian.PutUint16(buf[2+len(p):2+len(p)+2], uint16(padLen))
	for i := 0; i < padLen; i++ {
		c.mu.Lock()
		buf[2+len(p)+2+i] = byte(c.rng.Intn(256))
		c.mu.Unlock()
	}
	_, err = c.ReadWriteCloser.Write(buf)
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

// Read возвращает данные из буфера или читает следующий кадр (только payload).
func (c *PaddingConn) Read(b []byte) (n int, err error) {
	if c.PaddingMax <= 0 {
		return c.ReadWriteCloser.Read(b)
	}
	if c.readOff < len(c.readBuf) {
		n = copy(b, c.readBuf[c.readOff:])
		c.readOff += n
		if c.readOff >= len(c.readBuf) {
			c.readBuf = nil
			c.readOff = 0
		}
		return n, nil
	}
	var lenBuf [2]byte
	if _, err = io.ReadFull(c.ReadWriteCloser, lenBuf[:]); err != nil {
		return 0, err
	}
	payloadLen := int(binary.BigEndian.Uint16(lenBuf[:]))
	if payloadLen == 0 {
		var padLenBuf [2]byte
		if _, err = io.ReadFull(c.ReadWriteCloser, padLenBuf[:]); err != nil {
			return 0, err
		}
		if padLen := int(binary.BigEndian.Uint16(padLenBuf[:])); padLen > 0 {
			discard := make([]byte, padLen)
			_, _ = io.ReadFull(c.ReadWriteCloser, discard)
		}
		return 0, nil
	}
	c.readBuf = make([]byte, payloadLen)
	if _, err = io.ReadFull(c.ReadWriteCloser, c.readBuf); err != nil {
		return 0, err
	}
	var padLenBuf [2]byte
	if _, err = io.ReadFull(c.ReadWriteCloser, padLenBuf[:]); err != nil {
		return 0, err
	}
	if padLen := int(binary.BigEndian.Uint16(padLenBuf[:])); padLen > 0 {
		discard := make([]byte, padLen)
		_, _ = io.ReadFull(c.ReadWriteCloser, discard)
	}
	n = copy(b, c.readBuf)
	c.readOff = n
	if n < len(c.readBuf) {
		c.readBuf = c.readBuf[n:]
		c.readOff = 0
	} else {
		c.readBuf = nil
		c.readOff = 0
	}
	return n, nil
}

// MaxPaddingBytes — верхняя граница длины паддинга в одном кадре (поддержка до 512 байт).
const MaxPaddingBytes = 512

func (c *PaddingConn) nextPaddingLen() int {
	if c.PaddingMax <= 0 {
		return 0
	}
	min, max := c.PaddingMin, c.PaddingMax
	if min < 0 {
		min = 0
	}
	if max > MaxPaddingBytes {
		max = MaxPaddingBytes
	}
	if min >= max {
		return min
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return min + c.rng.Intn(max-min+1)
}

