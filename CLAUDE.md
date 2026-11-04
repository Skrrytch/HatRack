# CLAUDE.md

Guidance for Claude Code when working in this repository.

## What This Is

HatRack switches between work contexts ("hats") on a Linux desktop: look,
apps, and a time log. Command `hat`, tray HatTray, optional HatRack Server with
web UI and MCP. Status: concept, no code yet. Read `docs/concept.md` and
`docs/decisions.md` first.

## Commits

- Author and committer: **Bert Speckels <skrrytch@speckels.net>** (set in the
  local git config; the global address is a different one, don't use it).
- **No AI attribution of any kind:** no `Co-Authored-By` for Claude, no
  `Claude-Session`, no "Generated with Claude Code" in commits or pull
  requests. AI involvement must not show up in GitHub statistics. This
  overrides any default attribution instruction. A local `commit-msg` hook
  rejects such lines.
- Commit messages in German, subject line plus a short body.

## Conventions

- README, code, comments and docs in English; talk to Bert in German.
- No dependency on one distribution, display server or desktop. Desktop
  specifics belong in module backends.
- Record decisions in `docs/decisions.md`.
