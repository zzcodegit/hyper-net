# Contributing to Hypernet

Thank you for your interest in contributing.

This is a serious distributed systems project. Contributions must respect architectural boundaries.

---

## Principles

1. Do not break layer separation.
2. Do not introduce circular dependencies.
3. Network layer must not depend on economy layer.
4. Avoid global state.
5. Prefer interfaces.
6. Keep modules testable.

---

## Before Opening a PR

- Ensure tests pass (`go test ./...`)
- Add tests for new logic
- Run formatting (`go fmt`)
- Avoid large unrelated refactors

---

## Commit Style

Small, atomic commits.

Clear messages:

- `core: refactor transport abstraction`
- `proxy: add negotiation fallback test`
- `daemon: split bootstrap logic`

---

## Code Reviews

We review for:

- Architectural integrity
- Simplicity
- Safety
- Test coverage

---

## Discussions

Use GitHub Issues for:

- Architectural proposals
- Refactor plans
- Major feature ideas

Avoid introducing major features without prior discussion.

---

## Security

If you discover a security issue, do not open a public issue immediately.
Contact maintainers directly.

---

## Philosophy

We build in public.

We prioritize correctness over hype.

We prefer stable infrastructure over fast feature expansion.