package dns

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/peer"
)

const (
	// NamespacePrefix — префикс ключа в DHT для записей имён.
	NamespacePrefix = "/hypernet/name/"
	// NamePattern — допустимые символы в имени: буквы, цифры, дефис.
	NamePattern = `^[a-z0-9][a-z0-9-]*[a-z0-9]$|^[a-z0-9]$`
)

var nameRegex = regexp.MustCompile(NamePattern)

// Record — запись о привязке имени к ноде (сериализуется в DHT).
type Record struct {
	Name      string   `json:"name"`
	Owner     string   `json:"owner"`      // Peer ID владельца
	Addrs     []string `json:"addrs"`     // multiaddr в строковом виде
	TTL       int64    `json:"ttl"`       // время жизни в секундах
	Timestamp int64    `json:"timestamp"` // Unix time создания/обновления
	Signature string   `json:"signature"` // base64 подпись (Ed25519) полей без signature
}

// signablePayload возвращает байты для подписи: каноничное JSON без поля signature.
func (r *Record) signablePayload() ([]byte, error) {
	return json.Marshal(struct {
		Name      string   `json:"name"`
		Owner     string   `json:"owner"`
		Addrs     []string `json:"addrs"`
		TTL       int64    `json:"ttl"`
		Timestamp int64    `json:"timestamp"`
	}{
		Name: r.Name, Owner: r.Owner, Addrs: r.Addrs, TTL: r.TTL, Timestamp: r.Timestamp,
	})
}

// Sign подписывает запись приватным ключом владельца. Подпись пишется в r.Signature.
func (r *Record) Sign(privKey crypto.PrivKey) error {
	payload, err := r.signablePayload()
	if err != nil {
		return fmt.Errorf("dns record sign payload: %w", err)
	}
	sig, err := privKey.Sign(payload)
	if err != nil {
		return fmt.Errorf("dns record sign: %w", err)
	}
	r.Signature = base64.StdEncoding.EncodeToString(sig)
	return nil
}

// Verify проверяет подпись записи, используя публичный ключ из owner (Peer ID).
// pubKey можно получить через dht.GetPublicKey(ctx, owner) или из peerstore.
func (r *Record) Verify(pubKey crypto.PubKey) error {
	if r.Signature == "" {
		return fmt.Errorf("dns record: missing signature")
	}
	sig, err := base64.StdEncoding.DecodeString(r.Signature)
	if err != nil {
		return fmt.Errorf("dns record: invalid signature encoding: %w", err)
	}
	payload, err := r.signablePayload()
	if err != nil {
		return fmt.Errorf("dns record verify payload: %w", err)
	}
	ok, err := pubKey.Verify(payload, sig)
	if err != nil {
		return fmt.Errorf("dns record verify: %w", err)
	}
	if !ok {
		return fmt.Errorf("dns record: signature verification failed")
	}
	return nil
}

// Expired возвращает true, если запись просрочена (текущее время > timestamp + ttl).
func (r *Record) Expired(now time.Time) bool {
	return r.TTL <= 0 || now.Unix() > r.Timestamp+r.TTL
}

// NormalizeName приводит имя к нижнему регистру и проверяет формат.
// Допустимы только [a-z0-9-], имя не должно быть пустым.
func NormalizeName(name string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(name))
	if s == "" {
		return "", fmt.Errorf("dns: empty name")
	}
	if !nameRegex.MatchString(s) {
		return "", fmt.Errorf("dns: invalid name %q (allowed: [a-z0-9-])", name)
	}
	return s, nil
}

// DHTKey возвращает ключ DHT для данного имени (нормализованного).
func DHTKey(name string) string {
	return NamespacePrefix + name
}

// OwnerPeerID возвращает Peer ID владельца из записи.
func (r *Record) OwnerPeerID() (peer.ID, error) {
	return peer.Decode(r.Owner)
}
