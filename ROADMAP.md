# Hypernet Roadmap

---

## Phase 1 — Architecture Stabilization

- Complete migration from `pkg/` to `hypernet/`
- Refactor daemon composition
- Remove legacy coupling
- Add integration cluster tests
- 24-hour stress test stability

---

## Phase 2 — Identity & Subscription Hook

- Introduce UID abstraction
- Add subscription validation interface
- Enforce subscription check at proxy layer
- Role-based service enablement

---

## Phase 3 — Blockchain Client (Read-Only)

- Implement blockchain client (subscription read)
- Connect subscription validator
- Add graceful failure handling

---

## Phase 4 — Network Hardening

- NAT traversal improvements
- Relay optimization
- Routing improvements
- Resource limits tuning

---

## Phase 5 — Internal Services Expansion

- Identity-bound access control
- Domain registration logic
- CDN acceleration layer
- Media transport experiments

---

## Long-Term Vision

- Fully decentralized internal internet
- Token-based incentive model
- Global node network
- Scalable P2P hosting infrastructure