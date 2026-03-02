package shadowsocks

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"io"
	"sync"
)

const nonceSize = 12

// deriveKey returns 32 bytes (AES-256) from password.
func deriveKey(password string) []byte {
	h := sha256.Sum256([]byte(password))
	return h[:]
}

// ssAEAD wraps io.ReadWriteCloser with AES-256-GCM per-frame encryption.
// Frame format: [nonce 12][length 2 BE][ciphertext][tag 16]; length = len(ciphertext)+16.
type ssAEAD struct {
	io.ReadWriteCloser
	aead cipher.AEAD
	// read: buffer decrypted data from the last frame
	readBuf []byte
	readOff int
	mu      sync.Mutex
}

func newSSAEAD(rwc io.ReadWriteCloser, password string) (io.ReadWriteCloser, error) {
	key := deriveKey(password)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &ssAEAD{
		ReadWriteCloser: rwc,
		aead:            aead,
		readBuf:         nil,
		readOff:         0,
	}, nil
}

func (s *ssAEAD) Write(p []byte) (n int, err error) {
	if len(p) == 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// One frame per Write to keep it simple
	nonce := make([]byte, nonceSize)
	if _, err = rand.Read(nonce); err != nil {
		return 0, err
	}
	ciphertext := s.aead.Seal(nil, nonce, p, nil)
	// length = ciphertext (includes tag)
	if len(ciphertext) > 0xFFFF {
		return 0, io.ErrShortWrite
	}
	header := make([]byte, nonceSize+2)
	copy(header, nonce)
	binary.BigEndian.PutUint16(header[nonceSize:], uint16(len(ciphertext)))
	if _, err = s.ReadWriteCloser.Write(header); err != nil {
		return 0, err
	}
	if _, err = s.ReadWriteCloser.Write(ciphertext); err != nil {
		return 0, err
	}
	return len(p), nil
}

func (s *ssAEAD) Read(p []byte) (n int, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readOff < len(s.readBuf) {
		n = copy(p, s.readBuf[s.readOff:])
		s.readOff += n
		if s.readOff >= len(s.readBuf) {
			s.readBuf = nil
			s.readOff = 0
		}
		return n, nil
	}
	// Read next frame: nonce + length
	header := make([]byte, nonceSize+2)
	if _, err = io.ReadFull(s.ReadWriteCloser, header); err != nil {
		return 0, err
	}
	payloadLen := binary.BigEndian.Uint16(header[nonceSize:])
	if payloadLen == 0 || payloadLen > 0xFFFF {
		return 0, io.EOF
	}
	ciphertext := make([]byte, payloadLen)
	if _, err = io.ReadFull(s.ReadWriteCloser, ciphertext); err != nil {
		return 0, err
	}
	plaintext, err := s.aead.Open(nil, header[:nonceSize], ciphertext, nil)
	if err != nil {
		return 0, err
	}
	s.readBuf = plaintext
	s.readOff = 0
	n = copy(p, plaintext)
	s.readOff = n
	if s.readOff >= len(s.readBuf) {
		s.readBuf = nil
		s.readOff = 0
	}
	return n, nil
}
