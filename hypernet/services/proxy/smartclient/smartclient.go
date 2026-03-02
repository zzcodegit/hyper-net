package smartclient

import (
	"context"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"hypernet-node/pkg/client"
	"hypernet-node/hypernet/core/routing"

	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

// SmartClient — DHT-aware обёртка над ProxyClient.
// Он умеет находить ноды, поддерживающие указанный протокол, через DHT
// и пробовать подключаться к ним, используя DialTCPWithFallback.
type SmartClient struct {
	Discovery        routing.ProviderDiscovery
	VLESSAuthToken   string
	MaxProviders     int
	LatencyThreshold time.Duration

	// PreferredRegion — желаемый регион для выбора нод (может быть пустым).
	PreferredRegion string

	// ProviderCacheTTL задаёт время жизни записей кэша провайдеров.
	// Если 0, используется значение по умолчанию (5 минут).
	ProviderCacheTTL time.Duration

	mu            sync.Mutex
	providerCache map[string]providerCacheEntry
	peerStats     map[peer.ID]*peerStats
}

// providerCacheEntry хранит список провайдеров и время его получения из DHT.
type providerCacheEntry struct {
	providers []peer.AddrInfo
	fetchedAt time.Time
}

// peerStats хранит минимальные метрики доступности/латентности по ноде.
type peerStats struct {
	lastSuccess time.Time
	lastLatency time.Duration
	failures    int
	lastFailure time.Time
}

// NewSmartClient создаёт SmartClient поверх уже инициализированной ноды с DHT.
// Если MaxProviders или LatencyThreshold равны нулю, используются разумные
// значения по умолчанию.
func NewSmartClient(n routing.ProviderDiscovery, vlessToken string) *SmartClient {
	return &SmartClient{
		Discovery:        n,
		VLESSAuthToken:   vlessToken,
		MaxProviders:     8,
		LatencyThreshold: 2 * time.Second,
		ProviderCacheTTL: 5 * time.Minute,
		providerCache:    make(map[string]providerCacheEntry),
		peerStats:        make(map[peer.ID]*peerStats),
	}
}

// buildMultiaddr строит /ip4/.../tcp/.../p2p/<peerID> на основе AddrInfo.
func buildMultiaddr(info peer.AddrInfo) (string, error) {
	if len(info.Addrs) == 0 {
		return "", fmt.Errorf("no addresses for peer %s", info.ID)
	}
	maddr := info.Addrs[0]
	full, err := multiaddr.NewMultiaddr(fmt.Sprintf("%s/p2p/%s", maddr.String(), info.ID.String()))
	if err != nil {
		return "", err
	}
	return full.String(), nil
}

// DialTCPViaProtocol ищет через DHT провайдеров, объявивших поддержку
// указанного протокола (например, "vless" или "trojan"), и по очереди
// пробует установить TCP‑туннель до host:port через каждую найденную ноду.
// Для каждой ноды используется ProxyClient.DialTCPWithFallback, чтобы при
// необходимости переключиться на другой протокол этой же ноды.
func (s *SmartClient) DialTCPViaProtocol(ctx context.Context, protoName, host string, port int) (io.ReadWriteCloser, error) {
	if s == nil || s.Discovery == nil {
		return nil, fmt.Errorf("smartclient: discovery is nil")
	}
	if protoName == "" {
		return nil, fmt.Errorf("smartclient: empty protocol name")
	}

	max := s.MaxProviders
	if max <= 0 {
		max = 8
	}

	// Сначала пробуем взять провайдеров из кэша, если он ещё не протух.
	providers, fromCache := s.getCachedProviders(protoName)
	if len(providers) == 0 {
		var err error
		providers, err = s.fetchProviders(ctx, protoName, max)
		if err != nil {
			return nil, err
		}
		fromCache = false
	}

	if len(providers) == 0 {
		return nil, fmt.Errorf("smartclient: no providers found for protocol %q", protoName)
	}

	// Сортируем провайдеров по историческим метрикам (успешность/латентность),
	// чтобы в первую очередь пробовать ближайшие и надёжные ноды.
	providers = s.orderProviders(protoName, providers)

	tryProviders := func(list []peer.AddrInfo) (io.ReadWriteCloser, error) {
		var lastErr error
		for _, info := range list {
			maddr, err := buildMultiaddr(info)
			if err != nil {
				lastErr = err
				continue
			}

			pc, err := client.NewProxyClient(s.Discovery.Host(), maddr, s.VLESSAuthToken)
			if err != nil {
				lastErr = err
				s.recordFailure(info.ID)
				continue
			}

			start := time.Now()
			stream, err := pc.DialTCPWithFallback(ctx, host, port, s.LatencyThreshold)
			if err != nil {
				lastErr = err
				s.recordFailure(info.ID)
				continue
			}

			s.recordSuccess(info.ID, time.Since(start))

			// Успешное подключение через одну из нод‑провайдеров.
			return stream, nil
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("smartclient: all provider attempts failed for protocol %q", protoName)
		}
		return nil, lastErr
	}

	// Сначала пытаемся использовать текущий список (кэш или свежий).
	stream, err := tryProviders(providers)
	if err == nil {
		return stream, nil
	}

	// Если мы уже ходили в DHT и получили свежий список — просто возвращаем ошибку.
	// Если же изначально использовали кэш, пробуем один раз обновить список из DHT.
	if !fromCache {
		return nil, err
	}

	freshProviders, ferr := s.fetchProviders(ctx, protoName, max)
	if ferr != nil {
		// Возвращаем исходную ошибку попыток подключения, а не ошибку DHT.
		return nil, err
	}
	if len(freshProviders) == 0 {
		return nil, err
	}

	return tryProviders(freshProviders)
}

// getCachedProviders возвращает закэшированных провайдеров для протокола,
// если запись ещё не протухла. Второе значение == true, если данные
// действительно взяты из кэша.
func (s *SmartClient) getCachedProviders(protoName string) ([]peer.AddrInfo, bool) {
	ttl := s.ProviderCacheTTL
	if ttl <= 0 {
		ttl = 5 * time.Minute
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.providerCache[protoName]
	if !ok || len(entry.providers) == 0 {
		return nil, false
	}
	if time.Since(entry.fetchedAt) > ttl {
		return nil, false
	}

	// Возвращаем копию слайса, чтобы избежать неожиданных изменений снаружи.
	out := make([]peer.AddrInfo, len(entry.providers))
	copy(out, entry.providers)
	return out, true
}

// fetchProviders запрашивает провайдеров из DHT и обновляет кэш (если список не пуст).
func (s *SmartClient) fetchProviders(ctx context.Context, protoName string, max int) ([]peer.AddrInfo, error) {
	var providers []peer.AddrInfo
	var err error

	// Если задан предпочтительный регион, сначала пробуем найти провайдеров в нём.
	if s.PreferredRegion != "" {
		providers, err = s.Discovery.FindRegionProtocolProviders(ctx, protoName, s.PreferredRegion, max)
		if err != nil {
			return nil, fmt.Errorf("smartclient: FindRegionProtocolProviders(%q,%q): %w", protoName, s.PreferredRegion, err)
		}
	}

	// Если регион не задан или в нём никого не нашли, используем обычный поиск.
	if len(providers) == 0 {
		providers, err = s.Discovery.FindProtocolProviders(ctx, protoName, max)
	}
	if err != nil {
		return nil, fmt.Errorf("smartclient: FindProtocolProviders(%q): %w", protoName, err)
	}

	// Fallback для маленьких/локальных сетей: если DHT пока не вернул провайдеров,
	// пробуем использовать уже подключённых пиров как кандидатов.
	if len(providers) == 0 {
		for _, pid := range s.Discovery.ConnectedPeers() {
			addrs := s.Discovery.PeerAddrs(pid)
			if len(addrs) == 0 {
				continue
			}
			providers = append(providers, peer.AddrInfo{
				ID:    pid,
				Addrs: addrs,
			})
		}
	}

	// Обновляем кэш только если получили непустой список.
	if len(providers) > 0 {
		s.mu.Lock()
		if s.providerCache == nil {
			s.providerCache = make(map[string]providerCacheEntry)
		}
		s.providerCache[protoName] = providerCacheEntry{
			providers: providers,
			fetchedAt: time.Now(),
		}
		s.mu.Unlock()
	}

	return providers, nil
}

// recordSuccess обновляет статистику по пиру при успешном подключении.
func (s *SmartClient) recordSuccess(id peer.ID, lat time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.peerStats[id]
	if !ok {
		st = &peerStats{}
		s.peerStats[id] = st
	}
	st.lastSuccess = time.Now()
	st.lastLatency = lat
}

// recordFailure увеличивает счётчик неудач по пиру.
func (s *SmartClient) recordFailure(id peer.ID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.peerStats[id]
	if !ok {
		st = &peerStats{}
		s.peerStats[id] = st
	}
	st.failures++
	st.lastFailure = time.Now()
}

// orderProviders упорядочивает список провайдеров с учётом накопленной статистики.
// Ноды с меньшим количеством недавних отказов и меньшей латентностью идут первыми.
func (s *SmartClient) orderProviders(protoName string, in []peer.AddrInfo) []peer.AddrInfo {
	out := make([]peer.AddrInfo, len(in))
	copy(out, in)

	s.mu.Lock()
	defer s.mu.Unlock()

	type scored struct {
		idx   int
		score float64
	}

	var scores []scored
	now := time.Now()

	for i, info := range out {
		st, ok := s.peerStats[info.ID]
		if !ok {
			// Новые ноды — средний приоритет.
			scores = append(scores, scored{idx: i, score: 100.0})
			continue
		}
		score := 100.0
		// Латентность в миллисекундах добавляет к счёту.
		if st.lastLatency > 0 {
			score += float64(st.lastLatency.Milliseconds())
		}
		// Недавние отказы увеличивают штраф.
		if st.failures > 0 {
			age := now.Sub(st.lastFailure).Minutes()
			penalty := float64(st.failures) * 50.0
			if age < 30 {
				penalty *= 2
			}
			score += penalty
		}
		scores = append(scores, scored{idx: i, score: score})
	}

	sort.SliceStable(out, func(i, j int) bool {
		var si, sj float64
		for _, sc := range scores {
			if sc.idx == i {
				si = sc.score
			}
			if sc.idx == j {
				sj = sc.score
			}
		}
		return si < sj
	})

	return out
}

