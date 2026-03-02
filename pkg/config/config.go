package config

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// Config holds runtime configuration for a node.
type Config struct {
	Port           int
	KeyPath        string
	BootstrapPeers []string
	EnableRelay    bool
	EnableDHT      bool

	// DHTMode controls whether the node runs a full DHT server or a lightweight client.
	// Allowed values: "server", "client", "auto" (default).
	DHTMode string

	// Region — произвольный идентификатор региона/зоны (например, "eu", "us-east-1"),
	// который может использоваться для выбора ближайших нод на клиенте.
	Region string

	// DisableQUIC при true отключает прослушивание QUIC/UDP‑адресов и оставляет только TCP.
	// Используется для профилирования и отладки.
	DisableQUIC bool

	// GeoIPDBPath — путь к GeoIP2/GeoLite2 базе (MMDB) для определения региона клиента по IP.
	// Если пусто, определение client_region будет отключено.
	GeoIPDBPath string

	// MetricsDBPath — путь к SQLite-базе для хранения сессий/метрик.
	MetricsDBPath string

	// MetricsFlushInterval — интервал сброса буфера метрик в SQLite.
	MetricsFlushInterval time.Duration

	// MetricsQualityInterval — базовый интервал сбора quality‑метрик (RTT/потери и т.п.).
	MetricsQualityInterval time.Duration

	// CLI helpers (not part of libp2p setup itself)
	Target    string
	PingCount int

	// Relay limits (used when EnableRelay is true).
	// If zero or negative, library defaults are used.
	RelayMaxConnections int   // maps to MaxCircuits
	RelayDataLimitBytes int64 // per-direction data limit for relayed connections

	// DHT DNS: человекочитаемые имена ноды в сети (регистрируются в DHT).
	// DNS_NAMES — список имён через запятую; DNS_TTL — время жизни записи; DNS_REFRESH_INTERVAL — интервал обновления.
	DNSNames           []string
	DNSTTL             time.Duration
	DNSRefreshInterval time.Duration

	// Inbound: SOCKS5, HTTP-прокси, Dokodemo-door. Пустая строка = не слушать.
	Socks5Listen    string
	HTTPProxyListen string
	Outbound        string
	DokodemoListen  string // адрес слушать (например 127.0.0.1:12345)
	DokodemoTarget  string // куда редиректить (host:port)

	// REALITY (маскировка под реальный HTTPS): если заданы — нода поднимает REALITY-листенер.
	RealityListen    string   // адрес (например :443)
	RealityDest      string   // dest для маскировки (например www.microsoft.com:443)
	RealityPrivateKey string  // X25519 приватный ключ (Base64)
	RealityServerNames []string // SNI через запятую
	RealityShortIds   []string // short ID через запятую (hex)

	// ProxyProtocols — список прикладных протоколов прокси через запятую (порядок = приоритет).
	// Пример: "vless,trojan,plain". Пусто = все доступные в порядке по умолчанию.
	ProxyProtocols []string
}

// Load reads configuration from .env (if present), environment variables and command-line flags.
// Precedence: flags override environment variables; environment variables override built-in defaults.
func Load() (*Config, error) {
	// Load .env if present; ignore error if file is missing.
	_ = godotenv.Load()

	portDefault := envInt("PORT", 4001)
	keyPathDefault := envString("KEY_PATH", "node.key")
	bootstrapDefault := envString("BOOTSTRAP_PEERS", "")
	relayDefault := envBool("RELAY", false)
	dhtDefault := envBool("DHT", false)
	targetDefault := envString("TARGET", "")
	pingCountDefault := envInt("PING_COUNT", 1)
	dhtModeDefault := envString("DHT_MODE", "auto")
	relayMaxConnsDefault := envInt("RELAY_MAX_CONNECTIONS", 0)
	relayDataLimitDefault := envInt64("RELAY_DATA_LIMIT", 0)
	regionDefault := envString("REGION", "")
	disableQuicDefault := envBool("DISABLE_QUIC", false)
	geoIPDefault := envString("GEOIP_DB_PATH", "")
	metricsDBDefault := envString("METRICS_DB_PATH", "./hypernet.db")
	metricsFlushDefault := envDuration("METRICS_FLUSH_INTERVAL", "1s")
	metricsQualityDefault := envDuration("METRICS_QUALITY_INTERVAL", "30s")

	port := flag.Int("port", portDefault, "TCP port to listen on")
	keyPath := flag.String("key-path", keyPathDefault, "path to private key file")
	bootstrapPeers := flag.String("bootstrap-peers", bootstrapDefault, "comma-separated list of bootstrap peer multiaddrs")
	enableRelay := flag.Bool("relay", relayDefault, "enable relay mode (run circuit relay v2 service)")
	enableDHT := flag.Bool("dht", dhtDefault, "enable DHT (Kademlia)")
	dhtMode := flag.String("dht-mode", dhtModeDefault, "DHT mode: server|client|auto")
	region := flag.String("region", regionDefault, "logical region/zone label for this node (e.g. eu, us-east-1)")
	disableQuic := flag.Bool("disable-quic", disableQuicDefault, "disable QUIC/UDP transport (TCP only)")
	geoIPDBPath := flag.String("geoip-db-path", geoIPDefault, "path to GeoIP2/GeoLite2 MMDB file for client region detection")
	metricsDBPath := flag.String("metrics-db-path", metricsDBDefault, "path to SQLite DB for sessions/metrics")
	metricsFlush := flag.Duration("metrics-flush-interval", metricsFlushDefault, "flush interval for metrics SQLite writer (e.g. 500ms, 1s)")
	metricsQuality := flag.Duration("metrics-quality-interval", metricsQualityDefault, "base interval for quality samples (e.g. 30s)")
	target := flag.String("target", targetDefault, "target multiaddr (with /p2p/PeerID) to ping and exit")
	pingCount := flag.Int("ping-count", pingCountDefault, "number of ping messages to send when target is set")
	relayMaxConns := flag.Int("relay-max-connections", relayMaxConnsDefault, "maximum number of concurrent relayed connections per peer (0 = library default)")
	relayDataLimit := flag.Int64("relay-data-limit", relayDataLimitDefault, "per-connection data limit in bytes for relayed connections (0 = library default)")

	dnsNamesDefault := envString("DNS_NAMES", "")
	dnsTTLDefault := envDuration("DNS_TTL", "86400s")       // 86400 секунд = 24h
	dnsRefreshDefault := envDuration("DNS_REFRESH_INTERVAL", "43200s") // 43200 секунд = 12h
	dnsNames := flag.String("dns-names", dnsNamesDefault, "comma-separated list of DHT DNS names to register for this node")
	dnsTTL := flag.Duration("dns-ttl", dnsTTLDefault, "TTL for DHT DNS records (e.g. 24h)")
	dnsRefresh := flag.Duration("dns-refresh-interval", dnsRefreshDefault, "interval to refresh DHT DNS records (e.g. 12h)")

	socks5Default := envString("SOCKS5_LISTEN", "")
	httpProxyDefault := envString("HTTP_PROXY_LISTEN", "")
	outboundDefault := envString("OUTBOUND", "freedom")
	socks5Listen := flag.String("socks5-listen", socks5Default, "address to listen for SOCKS5 proxy (e.g. 127.0.0.1:1080), empty=disabled")
	httpProxyListen := flag.String("http-proxy-listen", httpProxyDefault, "address to listen for HTTP CONNECT proxy (e.g. 127.0.0.1:8080), empty=disabled")
	outbound := flag.String("outbound", outboundDefault, "outbound for inbound proxies: freedom or blackhole")
	dokodemoListenDefault := envString("DOKODEMO_LISTEN", "")
	dokodemoTargetDefault := envString("DOKODEMO_TARGET", "")
	dokodemoListen := flag.String("dokodemo-listen", dokodemoListenDefault, "address to listen for dokodemo-door (redirect), empty=disabled")
	dokodemoTarget := flag.String("dokodemo-target", dokodemoTargetDefault, "dokodemo-door redirect target (host:port)")

	realityListenDefault := envString("REALITY_LISTEN", "")
	realityDestDefault := envString("REALITY_DEST", "")
	realityPrivateKeyDefault := envString("REALITY_PRIVATE_KEY", "")
	realityServerNamesDefault := envString("REALITY_SERVER_NAMES", "")
	realityShortIdsDefault := envString("REALITY_SHORT_IDS", "")
	realityListen := flag.String("reality-listen", realityListenDefault, "REALITY listen address (e.g. :443), empty=disabled")
	realityDest := flag.String("reality-dest", realityDestDefault, "REALITY dest for masquerade (e.g. www.microsoft.com:443)")
	realityPrivateKey := flag.String("reality-private-key", realityPrivateKeyDefault, "REALITY X25519 private key (Base64)")
	realityServerNames := flag.String("reality-server-names", realityServerNamesDefault, "REALITY server names (SNI), comma-separated")
	realityShortIds := flag.String("reality-short-ids", realityShortIdsDefault, "REALITY short IDs (hex), comma-separated")

	proxyProtocolsDefault := envString("PROXY_PROTOCOLS", "")
	proxyProtocols := flag.String("proxy-protocols", proxyProtocolsDefault, "comma-separated proxy protocols and order (e.g. vless,trojan,plain); empty=all default")

	flag.Parse()

	if *port <= 0 || *port > 65535 {
		return nil, fmt.Errorf("invalid port: %d", *port)
	}

	var peers []string
	if trimmed := strings.TrimSpace(*bootstrapPeers); trimmed != "" {
		for _, p := range strings.Split(trimmed, ",") {
			s := strings.TrimSpace(p)
			if s != "" {
				peers = append(peers, s)
			}
		}
	}

	if *pingCount <= 0 {
		*pingCount = 1
	}

	cfg := &Config{
		Port:                *port,
		KeyPath:             *keyPath,
		BootstrapPeers:      peers,
		EnableRelay:         *enableRelay,
		EnableDHT:           *enableDHT,
		DHTMode:             normalizeMode(*dhtMode),
		Region:              strings.TrimSpace(*region),
		DisableQUIC:         *disableQuic,
		GeoIPDBPath:            strings.TrimSpace(*geoIPDBPath),
		MetricsDBPath:          strings.TrimSpace(*metricsDBPath),
		MetricsFlushInterval:   *metricsFlush,
		MetricsQualityInterval: *metricsQuality,
		Target:              *target,
		PingCount:           *pingCount,
		RelayMaxConnections: *relayMaxConns,
		RelayDataLimitBytes: *relayDataLimit,
	}
	if s := strings.TrimSpace(*dnsNames); s != "" {
		for _, name := range strings.Split(s, ",") {
			if n := strings.TrimSpace(name); n != "" {
				cfg.DNSNames = append(cfg.DNSNames, n)
			}
		}
	}
	cfg.DNSTTL = *dnsTTL
	cfg.DNSRefreshInterval = *dnsRefresh
	if cfg.DNSRefreshInterval <= 0 || cfg.DNSRefreshInterval > cfg.DNSTTL {
		cfg.DNSRefreshInterval = cfg.DNSTTL / 2
	}
	cfg.Socks5Listen = strings.TrimSpace(*socks5Listen)
	cfg.HTTPProxyListen = strings.TrimSpace(*httpProxyListen)
	cfg.Outbound = strings.TrimSpace(*outbound)
	if cfg.Outbound == "" {
		cfg.Outbound = "freedom"
	}
	cfg.DokodemoListen = strings.TrimSpace(*dokodemoListen)
	cfg.DokodemoTarget = strings.TrimSpace(*dokodemoTarget)
	cfg.RealityListen = strings.TrimSpace(*realityListen)
	cfg.RealityDest = strings.TrimSpace(*realityDest)
	cfg.RealityPrivateKey = strings.TrimSpace(*realityPrivateKey)
	if s := strings.TrimSpace(*realityServerNames); s != "" {
		for _, n := range strings.Split(s, ",") {
			if v := strings.TrimSpace(n); v != "" {
				cfg.RealityServerNames = append(cfg.RealityServerNames, v)
			}
		}
	}
	if s := strings.TrimSpace(*realityShortIds); s != "" {
		for _, id := range strings.Split(s, ",") {
			if v := strings.TrimSpace(id); v != "" {
				cfg.RealityShortIds = append(cfg.RealityShortIds, v)
			}
		}
	}
	if s := strings.TrimSpace(*proxyProtocols); s != "" {
		for _, p := range strings.Split(s, ",") {
			if v := strings.TrimSpace(strings.ToLower(p)); v != "" {
				cfg.ProxyProtocols = append(cfg.ProxyProtocols, v)
			}
		}
	}

	return cfg, nil
}

func envString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return i
}

func envBool(key string, def bool) bool {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "t", "yes", "y":
		return true
	case "0", "false", "f", "no", "n":
		return false
	default:
		return def
	}
}

// normalizeMode lower-cases and validates a DHT/role mode string.
// Unknown values fall back to "auto".
func normalizeMode(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "server":
		return "server"
	case "client":
		return "client"
	default:
		return "auto"
	}
}

func envInt64(key string, def int64) int64 {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		return def
	}
	i, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return def
	}
	return i
}

func envDuration(key string, def string) time.Duration {
	v, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(v) == "" {
		d, err := time.ParseDuration(def)
		if err != nil {
			return time.Second
		}
		return d
	}
	d, err := time.ParseDuration(strings.TrimSpace(v))
	if err != nil {
		dd, err2 := time.ParseDuration(def)
		if err2 != nil {
			return time.Second
		}
		return dd
	}
	return d
}


