package roles

// Role represents a high-level node role in the Hypernet overlay.
// This initial abstraction does not change runtime behavior; it will be
// gradually adopted by the daemon and configuration in later steps.
type Role string

const (
	// RoleRelay marks nodes that primarily serve as circuit relay v2 providers.
	RoleRelay Role = "relay"
	// RoleExit marks nodes that provide exit/VPN-style connectivity.
	RoleExit Role = "exit"
	// RoleOrigin marks nodes that primarily act as origin/application-serving nodes.
	RoleOrigin Role = "origin"
)

