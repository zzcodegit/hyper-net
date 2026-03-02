# Hypernet Architecture

This document provides a detailed technical breakdown of the Hypernet node implementation.

---

# Layered Architecture

## 1. Core (`hypernet/core`)

Pure networking kernel.

### identity

- Private key generation/loading
- libp2p identity wrapper

### network/node

- libp2p host creation
- DHT initialization
- Relay v2 / AutoRelay
- Bootstrap peer connection

### network/transport

Transport abstraction:

- TCP
- QUIC
- WebSocket
- KCP
- gRPC
- Unix sockets

### network/obfuscate

Traffic obfuscation:

- uTLS browser fingerprinting
- Padding
- Write delays

### network/ping

Ping protocol over libp2p.

### routing

Abstractions for DHT routing & discovery.

---

## 2. Services (`hypernet/services`)

Built strictly on top of core.

### proxy

Multi-protocol proxy server:

- VLESS
- VMess
- Trojan
- Shadowsocks
- Hysteria2
- Plain

Includes:

- Protocol negotiation
- SQLite metrics store
- Obfuscation negotiation
- Inbound implementations (SOCKS5, HTTP CONNECT, Dokodemo)

### dns

DHT-based decentralized naming:

- Signed records
- Multiaddr resolution
- DHT validators

### relay

Service-layer relay abstraction.

### vpn

REALITY integration (XTLS-based camouflage).

---

## 3. Economy (`hypernet/economy`)

Blockchain boundary layer.

### blockchain_client

Defines:

- Network abstraction
- SubscriptionStatus model
- Client interface
- Factory interface

No blockchain implementation yet.

### subscription

Future subscription validation logic.

### domain

Future economic models.

---

## 4. Node (`hypernet/node`)

Composition root.

### daemon

Responsible for:

- Host creation
- Service registration
- DNS publishing
- Proxy registration
- Inbound startup
- REALITY listener
- Graceful shutdown

### roles

Defines:

- Relay
- Exit
- Origin

Role enforcement is evolving.

---

# Architectural Principles

1. Network layer must not depend on blockchain.
2. Clear separation between core and services.
3. Interfaces over concrete implementations.
4. No circular dependencies.
5. Testable modules.
6. Graceful shutdown everywhere.

---

# Current Technical Focus

- Stabilizing transport layer
- Removing legacy `pkg/`
- Strengthening role enforcement
- Preparing subscription hook