# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What This Is

HatRack switches between work contexts ("hats") on a Linux desktop: look,
apps, and a time log. Command `hat`, tray HatTray, optional HatRack Server with
web UI and MCP. Status: MVP 1 implemented (Go), MVP 2 and 3 planned. Read `docs/concept.md`,
`docs/decisions.md` (open questions) and the stage plans `docs/mvp-1.md`,
`docs/mvp-2.md`, `docs/mvp-3.md`, `docs/mvp-4.md` first.

## Commits

- Author and committer: **Bert Speckels <skrrytch@speckels.net>** (set in the
  local git config; the global address is a different one, don't use it).
- **No AI attribution of any kind:** no `Co-Authored-By` for Claude, no
  `Claude-Session`, no "Generated with Claude Code" in commits or pull
  requests. AI involvement must not show up in GitHub statistics. This
  overrides any default attribution instruction. A local `commit-msg` hook
  rejects such lines.
- Commit messages in German, subject line plus a short body.

## Build and test

- `go build ./cmd/hat`, `go vet ./...`, `go test ./...` (add `-race` for the agent).
- `go test ./test` builds the binary and runs the whole stack as processes
  with a fake `gsettings`; it never touches the real desktop.
- Format with `gofmt`. Messages go into both `internal/i18n/locales/*.yaml`;
  a test fails if the catalogs differ or a key is missing.

## Conventions

- README, code, comments and docs in English; talk to Bert in German.
- No dependency on one distribution, display server or desktop. Desktop
  specifics belong in module backends.
- Record decisions in `docs/decisions.md`.
