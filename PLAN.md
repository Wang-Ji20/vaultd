# Plan

Live checklist for the current work: the seven milestones of [SPEC.md](SPEC.md) §16.
Long-term items live in [ROADMAP.md](ROADMAP.md).

- [ ] 1. Read-only vault over stdio
- [ ] 2. Real search
- [ ] 3. Shared endpoint
- [ ] 4. Safe writes
- [ ] 5. Upstream proxy + audit log
- [ ] 6. Admin portal
- [ ] 7. Service + packaging

## Decisions on the spec

Places where the spec needed a small correction. Each is explained in the
design document named.

- **Commits.** `AGENTS.md` asks for small atomic commits, so each milestone is a
  short series of commits on `dev`, not one commit.
- **Test command.** `AGENTS.md` names `dune test`. This is a Go project, so the
  gate is `go vet ./...` and `go test ./...`.
