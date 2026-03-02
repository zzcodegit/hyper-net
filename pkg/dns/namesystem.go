package dns

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"

	dht "github.com/libp2p/go-libp2p-kad-dht"
)

// CacheBypassKey — ключ контекста для принудительного игнорирования кэша при Resolve (для тестов).
// Использование: ctx = context.WithValue(ctx, dns.CacheBypassKey, true)
var CacheBypassKey = &struct{ s string }{s: "dns:cache_bypass"}

// ExtractPubKeyFromPeerID получает публичный ключ по Peer ID через DHT (libp2p-peer).
// Peer ID сам по себе не содержит ключ целиком; DHT хранит привязку ID -> PubKey.
func ExtractPubKeyFromPeerID(ctx context.Context, dht *dht.IpfsDHT, peerID peer.ID) (crypto.PubKey, error) {
	if dht == nil {
		return nil, fmt.Errorf("dns: dht is nil")
	}
	return dht.GetPublicKey(ctx, peerID)
}

// cacheEntry — элемент кэша разрешённого имени (адреса + время истечения).
type cacheEntry struct {
	addrs  []multiaddr.Multiaddr
	expiry time.Time
}

// NameSystem предоставляет регистрацию и разрешение имён через DHT.
type NameSystem struct {
	dht   *dht.IpfsDHT
	host  host.Host
	mu    sync.Mutex
	cache map[string]*cacheEntry
}

// NewNameSystem создаёт экземпляр для работы с именами.
func NewNameSystem(dht *dht.IpfsDHT, host host.Host) *NameSystem {
	return &NameSystem{
		dht:   dht,
		host:  host,
		cache: make(map[string]*cacheEntry),
	}
}

// Register объявляет имя для текущей ноды. addrs — список multiaddr ноды, ttl — время жизни записи.
// Если имя уже занято другой нодой, возвращается ошибка. Текущий владелец может перезаписать запись (обновление).
func (ns *NameSystem) Register(ctx context.Context, name string, addrs []multiaddr.Multiaddr, ttl time.Duration) error {
	if ns.dht == nil || ns.host == nil {
		return fmt.Errorf("dns: name system not initialized")
	}
	norm, err := NormalizeName(name)
	if err != nil {
		return err
	}
	if ttl <= 0 {
		return fmt.Errorf("dns: ttl must be positive")
	}
	addrStrs := make([]string, 0, len(addrs))
	for _, a := range addrs {
		addrStrs = append(addrStrs, a.String())
	}
	if len(addrStrs) == 0 {
		return fmt.Errorf("dns: at least one address required")
	}

	owner := ns.host.ID().String()
	now := time.Now().Unix()
	ttlSec := int64(ttl.Seconds())

	// Проверяем, не занято ли имя другой нодой
	existing, err := ns.dht.GetValue(ctx, DHTKey(norm))
	if err == nil && len(existing) > 0 {
		var rec Record
		if err := json.Unmarshal(existing, &rec); err == nil && !rec.Expired(time.Now()) {
			if rec.Owner != owner {
				return fmt.Errorf("dns: name %q already registered by another peer", name)
			}
			// Тот же владелец — обновление
		}
	}

	rec := &Record{
		Name:      norm,
		Owner:     owner,
		Addrs:     addrStrs,
		TTL:       ttlSec,
		Timestamp: now,
	}
	privKey := ns.host.Peerstore().PrivKey(ns.host.ID())
	if privKey == nil {
		return fmt.Errorf("dns: cannot get private key for signing")
	}
	if err := rec.Sign(privKey); err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("dns record marshal: %w", err)
	}
	if err := ns.dht.PutValue(ctx, DHTKey(norm), data); err != nil {
		return fmt.Errorf("dns put value: %w", err)
	}
	return nil
}

// Resolve разрешает имя в список multiaddr. Сначала проверяет кэш (если не истёк и не задан обход).
// Использует SearchValue для получения всех значений; из записей с корректной подписью и TTL
// выбирается наиболее свежая (по timestamp). Успешный результат кэшируется на время не более TTL записи.
func (ns *NameSystem) Resolve(ctx context.Context, name string) ([]multiaddr.Multiaddr, error) {
	if ns.dht == nil {
		return nil, fmt.Errorf("dns: name system not initialized")
	}
	norm, err := NormalizeName(name)
	if err != nil {
		return nil, err
	}

	// Кэш: при отсутствии CacheBypassKey в контексте проверяем кэш.
	bypassCache := ctx.Value(CacheBypassKey) != nil
	if !bypassCache {
		ns.mu.Lock()
		if e, ok := ns.cache[norm]; ok && e != nil && time.Now().Before(e.expiry) {
			addrs := append([]multiaddr.Multiaddr(nil), e.addrs...)
			ns.mu.Unlock()
			return addrs, nil
		}
		ns.mu.Unlock()
	}

	key := DHTKey(norm)
	ch, err := ns.dht.SearchValue(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("dns search value: %w", err)
	}
	now := time.Now()
	var best *Record
	for data := range ch {
		if len(data) == 0 {
			continue
		}
		var rec Record
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		if rec.Expired(now) {
			continue
		}
		ownerID, err := rec.OwnerPeerID()
		if err != nil {
			continue
		}
		pubKey, err := ExtractPubKeyFromPeerID(ctx, ns.dht, ownerID)
		if err != nil || pubKey == nil {
			continue
		}
		if err := rec.Verify(pubKey); err != nil {
			continue
		}
		if best == nil || rec.Timestamp > best.Timestamp {
			best = &rec
		}
	}
	if best == nil {
		return nil, fmt.Errorf("dns: имя не найдено")
	}
	var addrs []multiaddr.Multiaddr
	for _, s := range best.Addrs {
		a, err := multiaddr.NewMultiaddr(s)
		if err != nil {
			continue
		}
		addrs = append(addrs, a)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("dns: no valid addresses in record for %q", name)
	}

	// Кэшируем на время не более TTL записи (с небольшим запасом — минус 1 мин, чтобы не отдавать почти просроченное).
	cacheTTL := time.Duration(best.TTL) * time.Second
	if cacheTTL > time.Minute {
		cacheTTL -= time.Minute
	}
	expiry := time.Now().Add(cacheTTL)
	ns.mu.Lock()
	ns.cache[norm] = &cacheEntry{addrs: addrs, expiry: expiry}
	ns.mu.Unlock()

	return addrs, nil
}

// Unregister удаляет имя: публикует запись с пустыми адресами и нулевым TTL с подписью.
func (ns *NameSystem) Unregister(ctx context.Context, name string) error {
	if ns.dht == nil || ns.host == nil {
		return fmt.Errorf("dns: name system not initialized")
	}
	norm, err := NormalizeName(name)
	if err != nil {
		return err
	}
	owner := ns.host.ID().String()
	existing, err := ns.dht.GetValue(ctx, DHTKey(norm))
	if err == nil && len(existing) > 0 {
		var rec Record
		if err := json.Unmarshal(existing, &rec); err == nil && rec.Owner != owner {
			return fmt.Errorf("dns: name %q is owned by another peer", name)
		}
	}
	rec := &Record{
		Name:      norm,
		Owner:     owner,
		Addrs:     nil,
		TTL:       0,
		Timestamp: time.Now().Unix(),
	}
	privKey := ns.host.Peerstore().PrivKey(ns.host.ID())
	if privKey == nil {
		return fmt.Errorf("dns: cannot get private key for signing")
	}
	if err := rec.Sign(privKey); err != nil {
		return err
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	return ns.dht.PutValue(ctx, DHTKey(norm), data)
}

// Refresh обновляет запись: получает текущую из DHT, проверяет владельца,
// обновляет timestamp и адреса хоста, переподписывает и публикует.
func (ns *NameSystem) Refresh(ctx context.Context, name string) error {
	if ns.dht == nil || ns.host == nil {
		return fmt.Errorf("dns: name system not initialized")
	}
	norm, err := NormalizeName(name)
	if err != nil {
		return err
	}
	data, err := ns.dht.GetValue(ctx, DHTKey(norm))
	if err != nil {
		return fmt.Errorf("dns: get value for refresh: %w", err)
	}
	if len(data) == 0 {
		return fmt.Errorf("dns: no existing record for %q to refresh", name)
	}
	var rec Record
	if err := json.Unmarshal(data, &rec); err != nil {
		return fmt.Errorf("dns refresh unmarshal: %w", err)
	}
	owner := ns.host.ID().String()
	if rec.Owner != owner {
		return fmt.Errorf("dns: name %q is owned by another peer", name)
	}
	// Текущие адреса хоста с /p2p/PeerID
	var addrStrs []string
	for _, a := range ns.host.Addrs() {
		addrStrs = append(addrStrs, a.String()+"/p2p/"+ns.host.ID().String())
	}
	if len(addrStrs) == 0 {
		return fmt.Errorf("dns: host has no addresses")
	}
	rec.Addrs = addrStrs
	rec.Timestamp = time.Now().Unix()
	// TTL оставляем прежним
	if rec.TTL <= 0 {
		rec.TTL = 86400
	}
	privKey := ns.host.Peerstore().PrivKey(ns.host.ID())
	if privKey == nil {
		return fmt.Errorf("dns: cannot get private key for signing")
	}
	if err := rec.Sign(privKey); err != nil {
		return err
	}
	newData, err := json.Marshal(&rec)
	if err != nil {
		return err
	}
	return ns.dht.PutValue(ctx, DHTKey(norm), newData)
}
