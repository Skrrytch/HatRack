# HatRack

**Put on a hat, and your desktop, your apps and your time log follow.**

Most of us wear several hats in a day: the office job, a client project, a side
project, private stuff. HatRack makes the switch between them explicit. One
command or one click on the tray icon puts on a different hat, and with it a
different wallpaper and colour scheme, the apps and websites that belong to it,
and a new entry in the log. Over the day, that log becomes a time sheet: which
hat, which activity, how long.

```
hat on venom            # put on the VENOM hat
hat on venom do review  # ... with an activity
hat do meeting          # change the activity, keep the hat
hat off                 # pause: the look stays, the clock stops
hat on                  # resume
hat                     # what am I wearing, since when
hat log today           # today's overview
```

> **Status:** MVP 1 is implemented and waits for a test in daily use. MVP 2 and 3
> are plans. See [docs/concept.md](docs/concept.md),
> the stages [MVP 1](docs/mvp-1.md), [MVP 2](docs/mvp-2.md),
> [MVP 3 and outlook](docs/mvp-3.md), and the open questions in
> [docs/decisions.md](docs/decisions.md).

## Try it

Needs Go 1.24 or newer. One script builds, installs and restarts:

```
scripts/update.sh --setup     # first time: build, install to ~/.local/bin/hat,
                              # run server, agent and tray at login as user services
hat server device add laptop  # prints a token
hat agent login http://127.0.0.1:8421 <token>
hat server user set-password  # login for the web UI at http://127.0.0.1:8421
hat config import docs/example.yaml
hat tray show                 # tray icon (needs a StatusNotifier host); left click = panel
hat panel                     # the panel; bind this to a keyboard shortcut

hat on venom do review
```

After every change to the code: `scripts/update.sh` (add `--test` to run the
tests first). It rebuilds `hat` and restarts the services that are installed.

The server also runs as a Docker image: see [deploy/Dockerfile](deploy/Dockerfile).

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
