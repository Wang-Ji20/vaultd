# Plan

Live checklist for the current work: the seven milestones of [SPEC.md](SPEC.md) §16.
Long-term items live in [ROADMAP.md](ROADMAP.md).

- [x] 1. Read-only vault over stdio
- [x] 2. Real search
- [x] 3. Shared endpoint
- [x] 4. Safe writes
- [x] 5. Upstream proxy + audit log
- [x] 6. Admin portal
- [x] 7. Service + packaging

## Decisions on the spec

Places where the spec needed a small correction. Each is explained in the
design document named.

- **Commits.** `AGENTS.md` asks for small atomic commits, so each milestone is a
  short series of commits on `dev`, not one commit.
- **Test command.** `AGENTS.md` names `dune test`. This is a Go project, so the
  gate is `go vet ./...` and `go test ./...`.
- **Upstream descriptions.** SPEC §6 passes these through unchanged, and §8 wants
  all descriptions to warn that content is data. vaultd's own tools carry the warning
  in their descriptions. For upstream tools it sits in the server instructions, which
  apps see once per connection.
- **Search ranking.** SPEC §9 gives `bm25(notes_fts, 10.0, 5.0, 1.0)`. bm25 takes one
  weight per column and `path` is the first column, so these would rank title 5, tags 1,
  body 1. vaultd uses `bm25(notes_fts, 0.0, 10.0, 5.0, 1.0)`, which matches the title,
  tags and body weights the spec states. See `docs/design/search-index.md`.
- **Third-party files.** `AGENTS.md` says nothing third-party is vendored, and SPEC §3 and
  §12 embed htmx and Inter. The maintainer chose to allow exactly these two files, in
  `internal/admin/web/` with their licences. `AGENTS.md` itself has not been changed.
- **Colours and AA.** Several of the spec's light-mode colours fail WCAG AA as text. They stay
  for fills and marks, and text uses darker tokens. See `docs/design/admin.md`.
- **Windows file watching.** SPEC §3 names fsnotify, but on Windows its per-folder handles block
  renaming any folder with a subfolder. The maintainer chose a small watcher of our own on
  Windows only, with no new dependency. See `docs/design/search-index.md`.

## Not yet verified

- Installing and running the Windows Service (it needs administrator rights).
- Claude Desktop, Cursor and DeepSeek Harness themselves. Tests use the official SDK client.
- The GitHub Actions workflow, which has no remote to run on yet.
