package relay

import "hypernet-node/pkg/config"

// Config is a narrow, relay-focused view of the node configuration.
// It is intended for higher-level services/roles that need to decide whether
// a node should act as a relay and with which limits, without depending on
// the entire config.Config type.
type Config struct {
	// Enable indicates whether this node should run as a circuit relay v2 service.
	Enable bool

	// MaxConnections maps to the per-peer MaxCircuits limit.
	// Zero or negative means "use library defaults".
	MaxConnections int

	// DataLimitBytes is a per-connection data limit in bytes for relayed traffic.
	// Zero or negative means "use library defaults".
	DataLimitBytes int64

	// BootstrapPeers are candidate peers that can be used as static relays or
	// to bootstrap connectivity for relay nodes.
	BootstrapPeers []string
}

// FromNodeConfig builds a relay.Config from the shared node configuration.
// A nil input yields a zero-value Config with all fields disabled.
func FromNodeConfig(cfg *config.Config) Config {
	if cfg == nil {
		return Config{}
	}
	out := Config{
		Enable:         cfg.EnableRelay,
		MaxConnections: cfg.RelayMaxConnections,
		DataLimitBytes: cfg.RelayDataLimitBytes,
	}
	if len(cfg.BootstrapPeers) > 0 {
		out.BootstrapPeers = append([]string(nil), cfg.BootstrapPeers...)
	}
	return out
}

