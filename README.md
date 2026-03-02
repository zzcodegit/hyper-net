# Hypernet Node — Building a New Internet

Hypernet is an experimental decentralized P2P node implementation aimed at building a new internal internet — an overlay network on top of libp2p with proxy services, DHT-based DNS, VPN/exit capabilities, and a future economic layer.

Inspired partly by rewatching *Silicon Valley*, we asked a serious engineering question:

> What if building a new internet is actually possible?

This project is built publicly, transparently, and incrementally.

---

## 🚀 Vision

Hypernet is designed as infrastructure — not just a proxy.

We are building:

- A decentralized internal internet
- Peer-to-peer service hosting
- Identity-driven access control
- DHT-based domain resolution
- Integrated VPN/exit capabilities
- Future blockchain-backed subscription logic

Clean layering. No architectural shortcuts.

---

## 🧱 Architecture Summary

The project is structured into strict layers:

- **core** — pure networking kernel (identity, transport, DHT, routing)
- **services** — proxy, DNS, relay, VPN
- **economy** — blockchain boundaries and subscription interfaces
- **node** — composition root and role logic
- **apps** — CLI tools
- **cmd** — binary entrypoints
- **pkg** — legacy (being removed)

The network layer does **not** depend on blockchain logic.

For full technical breakdown, see [ARCHITECTURE.md](ARCHITECTURE.md).

---

## 🛠 Build & Run

Build node:

```bash
go build ./cmd/node
```

Build client:

```bash
go build ./cmd/client
```

Run tests:

```bash
go test ./...
```

Main entrypoints:

- `cmd/node/main.go`
- `hypernet/node/daemon/run.go`

---

## 🧭 Project Status

- Core networking: functional
- Proxy stack: functional
- DHT DNS: functional
- Role system: partial
- Economic layer: interface-only (no implementation yet)
- Architecture stabilization in progress

---

## 🌍 Why We’re Building This

We believe:

- Infrastructure should be open
- Networks should be resilient
- Identity should be user-controlled
- Services should not depend on centralized gatekeepers

We are building this publicly — part engineering challenge, part long-term experiment, part fun. But the architecture is serious.

---

## 💚 Support the Project

If you want to support development:

- **USDT (ERC20):** `0x0f5D74798AbaBf5397999AF17b4C446cdda2ce73`
- **Network:** ERC20

---

## 📚 Documentation

- [ARCHITECTURE.md](ARCHITECTURE.md) — full system breakdown
- [ROADMAP.md](ROADMAP.md) — development phases
- [CONTRIBUTING.md](CONTRIBUTING.md) — contribution rules