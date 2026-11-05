# HatRack

**Put on a hat, and your desktop, your apps and your time log follow.**

Most of us wear several hats in a day: the office job, a client project, a side
project, private stuff. HatRack makes the switch between them explicit. One
command or one click on the tray icon puts on a different hat, and with it a
different wallpaper and colour scheme, the apps and websites that belong to it,
and a new entry in the log. Over the day, that log becomes a time sheet: which
hat, which activity, how long.

```
hat on venom            # work on VENOM
hat on venom do review  # ... with an activity
hat do meeting          # change the activity, keep the hat
hat off                 # pause: the look stays, the clock stops
hat on                  # resume
hat                     # what am I working on, since when
hat log today           # today's overview
```

> **Status:** MVP 1 is implemented and waits for a test in daily use. MVP 2 and 3
> are plans. See [docs/concept.md](docs/concept.md),
> the stages [MVP 1](docs/mvp-1.md), [MVP 2](docs/mvp-2.md),
> [MVP 3 and outlook](docs/mvp-3.md), [MVP 4](docs/mvp-4.md), [MVP 5](docs/mvp-5.md), [MVP 6](docs/mvp-6.md), and the open questions in
> [docs/decisions.md](docs/decisions.md).

## Install

Works fully on Cinnamon (X11). On other desktops the time log, apps and
notifications work; the look does not change yet.

    curl -fsSL https://github.com/Skrrytch/HatRack/releases/latest/download/install.sh | sh

This puts the single binary `hat` into `~/.local/bin` (no root, nothing
system-wide, so removing it later is one file), then starts `hat setup`. Setup
asks where the server runs, starter hats and whether to show a tray icon; with
a server on this computer it also asks for a web password. It installs the
background services itself.

The server runs on this computer as a user service by default, so there is
nothing to host. To share one time log between computers, run it in a
container: copy [deploy/compose.yaml](deploy/compose.yaml) to the server, run
`docker compose up -d`, and create a token for each computer:

```
docker compose exec hatrack /hat server device add laptop   # prints the token
hat setup --server http://<server>:8421 --token <token>     # on the laptop
```

The compose file binds the port to `127.0.0.1`, so only that machine reaches
it. To reach it from other computers, change that port line and put TLS in
front first (for example a reverse proxy): the token goes with every request.

- **Debian, Ubuntu, Mint:** the `.deb` from the releases puts `hat` into
  `/usr/bin` with a menu entry; then run `hat setup` as well.
- **Update:** `hat update` (for the `.deb`: install the newer package; for the
  container: `docker compose pull && docker compose up -d`).
- **Remove:** `hat setup --remove`. Your log stays in `~/.local/share` until you
  add `--purge`.

Quick start after setup:

```
hat panel                     # quick switcher; bind it to a keyboard shortcut
hat on work do focus          # or switch from the command line
hat detail TAG-1366           # name the activity more precisely (optional)
```

An AI assistant such as Claude Code can read your hats and the time log over
MCP: create a token in Setup → AI access and run the `claude mcp add …` line
shown there; then ask "how much time did I spend on VENOM last week?".

## Build from source

Needs Go 1.24 or newer. `scripts/update.sh` builds, installs and restarts; add
`--setup` the first time and `--test` to run the tests first. After every
change to the code, run it again.

## Components

All parts are one Go binary, `hat`.

| Name | What | Command |
|---|---|---|
| **HatRack** | the tool as a whole | – |
| **hat** | command line | `hat` |
| **agent** | background service that applies hats | `hat agent` |
| **HatTray** | tray icon and menu | `hat tray run` |
| **HatRack Server** | optional central server: web UI, API, MCP for AI tools | `hat server` |

## Goals

- **Modules for everything.** Wallpaper, colour scheme, launching apps,
  notifications and scripts are modules. A module has one backend per desktop
  and drops out quietly where none exists.
- **Any distribution, any display server, any desktop.** No feature may depend
  on one of them. Some features come later for some desktops.
- **Server optional.** With a server, all devices share hats, activities and
  the log, and an AI can analyse your time through MCP. Without one, the same
  core runs locally.
- **The log is the truth.** Events are appended, never changed. Corrections are
  new events.

## License

[MIT](LICENSE) © 2026 Bert Speckels

## Wallpapers

The built-in wallpapers (`internal/assets/wallpapers`, two for each of the six
hat colours) are abstract colour gradients generated with Gemini for HatRack.
They show nothing and are covered by the project's MIT licence.
