// Package relay defines higher-level relay service configuration built on top of core networking.
//
// At this stage the actual libp2p relay wiring still lives in the core node package
// (hypernet/core/network/node). This package provides a focused view of relay-related
// configuration that higher-level roles/services can depend on without pulling in the
// entire node configuration or libp2p internals.
package relay
