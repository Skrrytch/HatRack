# MVP 1: Switch from the command line

The first version Bert uses every day. It covers the core loop: put on a hat,
the desktop follows, the switch is logged, the log becomes a time overview.
Everything else waits for [MVP 2](mvp-2.md).

## Capabilities

- **Switch hats from the command line.** `hat on venom`, `hat do review`,
  `hat off`, `hat`. Hat and activity names ignore case and accept any unique
  prefix (`hat on ve do rev`).
- **Activities are optional.** A hat can be worn without one. Per hat you
  choose whether the last activity used in that hat comes back on its own,
  only after asking, or not at all. Unknown activities are rejected.
- **Pause without losing the look.** `hat off` stops the clock but keeps the
  wallpaper and colours; the hat is still on the rack, just paused.
  `hat on` alone resumes it.
- **The desktop follows.** Wallpaper and colour scheme change with the hat,
  a notification confirms the switch, and any script can run on enter and
  leave. Backends in MVP 1: Cinnamon (Bert's desktop) plus D-Bus
  notifications and `exec`, which work everywhere.
- **Time log and overview.** Every switch is an event on the server.
  `hat log today` and `hat log week` show the timeline and the totals per hat
  and activity. A forgotten switch can be entered later with `--at 09:15`.
- **Configuration as YAML.** Hats, activities and module settings are written
  in a YAML file, imported with `hat config import` and exported with
  `hat config export`. Moving a setup to another server or machine is one
  export and one import.
- **Works offline.** If the server is unreachable, the switch still happens on
  the desktop and the events are sent later.
- **A first look in the browser.** The server shows a read-only "Now" page:
  current hat, activity, since when.
- **English and German.** CLI output, notifications and the "Now" page follow
  the locale or the browser language; `language: auto | en | de` overrides it.

## Components

All components live in one Go module and build into one binary, `hat`.

| Component | Where | Content in MVP 1 |
|---|---|---|
| Core | `internal/core`, `internal/store` | data model, event log, interval and total calculation, name resolution, config YAML (schema, import, export), module runtime, storage interface with SQLite |
| Server | `internal/server` (`hat server`) | REST API, SSE stream, device tokens, read-only "Now" page, Docker image |
| Agent | `internal/agent` (`hat agent`) | `systemd --user` service, SSE client, module host, offline queue, Unix socket |
| CLI | `internal/cli` (`hat`) | `on`, `do`, `off`, `status`, `log`, `config`, `agent`, `server` |
| Module `wallpaper` | built in | backend `cinnamon` |
| Module `color_scheme` | built in | backend `cinnamon` (GTK theme, light/dark preference) |
| Module `notify` | built in | backend `dbus` (org.freedesktop.Notifications) |
| Module `exec` | built in | runs a command on enter and leave |

Not in MVP 1: web UI beyond "Now", MCP, HatTray, `launch`, `mqtt`, GNOME and
KDE backends, editing the log, multiple devices in daily use (the design
supports them, testing focuses on one).

## How it works

### States

```
          hat on X                      hat off
 (none) ──────────► wearing(X, a?) ─────────────► paused(X)
                      │    ▲  ▲                     │
          hat do b    │    │  └─────────────────────┘
          hat on Y    └────┘     hat on  /  hat on X
```

- **none:** no hat was ever worn, the desktop is in its neutral state.
- **wearing(hat, activity?):** the clock runs for this hat and, if set, this
  activity.
- **paused(hat):** the clock stops, the look stays. Only the time log sees
  the pause.

There is exactly one state per user, shared by all devices (see
[decisions.md](decisions.md)).

Transitions that are not in the diagram:

| Command | none | paused(X) | wearing(X, a?) |
|---|---|---|---|
| `hat on <hat>` | wear | wear (also for X itself) | switch hat |
| `hat on` | error: no hat to resume | resume X | no change |
| `hat do <activity>` | error: no hat on | error: no hat on, use `hat on` first | change activity |
| `hat off` | error: no hat on | no change | pause |
| `hat off --neutral` | no change | restore the neutral look | pause and restore |

Errors never write an event.

**Resume.** `hat on` and `hat on X` for the paused hat X write a `switch`
event for the same hat. `hat on` without a name keeps the activity X had when
it was paused; `hat on X` follows X's `resume_activity` setting. The `enter` hooks run
only if the look is not currently applied, that is after `off --neutral`.
`off --neutral` runs the `leave` hooks of X; plain `off` runs none.

### Commands

| Command | Effect |
|---|---|
| `hat` / `hat status` | `VENOM · review · since 09:12 (1h 23m)` or `paused (VENOM) since 12:30` |
| `hat on <hat>` | put on the hat; the activity follows the hat's resume setting |
| `hat on <hat> do <activity>` | put on the hat with this activity |
| `hat on` | resume the paused hat |
| `hat do <activity>` | change the activity, keep the hat |
| `hat do -` | clear the activity, keep the hat |
| `hat off` | pause; the look stays |
| `hat off --neutral` | pause and restore the neutral look |
| `hat log [today\|yesterday\|week\|<date>]` | timeline and totals |
| `hat config export [-o file]` | write the configuration as YAML |
| `hat config import <file> [--dry-run] [--replace]` | read a configuration |
| `hat agent login <url> <token>` | connect this device to a server (writes `agent.yaml`, works without a running agent) |
| `hat agent run` / `hat agent install` | run the agent in the foreground / install and enable the `systemd --user` unit |
| `hat server [--listen addr] [--data dir]` | run the server |
| `hat server device add <name>` | register a device, print its token |
| `hat server backup <file>` | write a consistent copy of the database |

`hat server …` works directly on the server's data directory; it does not go
through the agent.

`on`, `do` and `off` accept `--at HH:MM` (or a full timestamp) for a switch
that was forgotten. The event gets that time; intervals are derived by
timestamp, so it slots in correctly.

### Name resolution

1. Compare case-insensitively with the hat's key and display name. Key and
   display name of the same hat count as one candidate, so `venom` and
   `VENOM` never clash.
2. An exact match wins.
3. Otherwise exactly one prefix match (counted per hat) wins.
4. Several hats match: error with the candidates. No match: error with the
   list.

Activities are resolved the same way, but only within the target hat's list.
An unknown activity is an error; the switch does not happen.

### Resuming activities

Each hat has `resume_activity: never | last | ask`, with a global default.

- **never:** `hat on venom` starts without an activity.
- **last:** the last activity used in this hat comes back.
- **ask:** in a terminal, the CLI asks `Resume "review"? [Y/n]`. Without a
  terminal (a desktop shortcut runs `hat on venom`), MVP 1 treats `ask` as
  `never`; MVP 2 asks through a notification with buttons.

An explicit `do <activity>` always wins.

### A switch, step by step

1. The CLI sends `on venom do review` to the agent over the Unix socket.
2. The agent resolves the names against its cached configuration and creates
   a `switch` event with a new UUID and the device's timestamp.
3. The agent applies the switch locally right away: `leave` hooks of the old
   hat, then `enter` hooks of the new one. The user sees the change at once,
   with or without a server. This only happens if the event is the newest
   one in the log. A backdated event (`--at`) that lands before the newest
   event is written to the log but leaves the look alone.
4. The agent posts the event to the server. If that fails, the event goes
   into the offline queue.
5. The server appends the event, updates the current state and pushes it to
   all agents over SSE.
6. Every agent compares the pushed state with what it has applied. The
   state carries the ID of the event it comes from. The sending agent
   recognises its own event ID and does nothing; other devices apply the
   switch.

Module hooks run only when the hat changes. Activity changes, pause and resume
write events but leave the look alone. A failing module is logged, reported as
a notification and a `module_error` event; the other modules still run.

### Time calculation

Intervals are derived from the event log, sorted by timestamp:

- `switch` closes the open interval and opens one for (hat, activity).
- `activity` closes the open interval and opens one for (same hat, new
  activity).
- `off` closes the open interval.
- `--at` events are ordinary events with an earlier timestamp. If one lands
  inside an existing interval, it splits it.

`hat log` shows the intervals of the period and the totals per hat and per
hat/activity. An interval that is still open counts up to now.

### Configuration

```yaml
version: 1
defaults:
  resume_activity: ask
modules:
  notify: {enabled: true}
hats:
  venom:
    name: VENOM
    color: "#7a1fa2"
    resume_activity: last
    activities: [development, review, meeting]
    modules:
      wallpaper: {image: ~/Pictures/hats/venom.jpg}
      color_scheme: {theme: Mint-Y-Dark-Purple, prefer: dark}
      exec: {enter: "~/bin/venom-on.sh", leave: "~/bin/venom-off.sh"}
  private:
    name: Private
    activities: []
```

- Hats are identified by their **key** (`venom`). Renaming the display name
  keeps history; changing the key creates a new hat. Activities are
  identified by their name within the hat: renaming one creates a new
  activity, the old one stays in the history.
- The order of hats and activities in the file is their sort order.
- **Import** merges by key: new hats and activities are added, existing ones
  updated, missing ones kept. `--replace` archives what the file does not
  contain (never deletes, because the log refers to it). `--dry-run` shows
  the changes first.
- **Export** writes the same format, so export → import is lossless.
- **Secrets** are never written in plain text. A value may be
  `${env:NAME}`; the export keeps the reference.
- **Device settings** (the third settings layer) stay out of the export
  unless `--with-devices` is given.
- **Files** such as wallpapers are referenced by path. On import, missing
  files are reported as warnings. A bundle with the files is part of MVP 2.

The format is the same on the server and, later, in standalone mode: import
and export live in the core (`internal/core`).

### Neutral state

On first start the agent asks every module for a `snapshot()` and stores it
locally. `hat off --neutral` (and uninstalling) restores it. The snapshot is
per device, because the neutral look of a laptop and a PC differ.

A snapshot has the same shape as the module's configuration, so restoring is
`Apply(snapshot)`; no separate restore function is needed. Modules without a
look (`notify`, `exec`) return no snapshot.

### Offline

- Events that cannot be sent are stored in a local queue (SQLite) with their
  UUID and device timestamp.
- The agent retries with backoff and sends the queue in order on reconnect.
- The server inserts them by timestamp. If another device switched in the
  meantime, the current state is the event with the latest timestamp, and the
  agent applies that state after the replay.

## Architecture

### Repository and packages

```
hatrack/
├─ cmd/hat/            main: subcommand dispatch
├─ internal/
│  ├─ core/            model, events, intervals, names, config (no desktop or network I/O)
│  ├─ store/           storage interface, SQLite implementation
│  ├─ agent/           socket server, SSE client, offline queue, module host
│  ├─ server/          HTTP API, SSE, "Now" page (templ + HTMX, embedded)
│  ├─ cli/             commands and output
│  └─ modules/         wallpaper, color_scheme, notify, exec (+ backends)
├─ web/                templ templates, static files (embedded)
├─ deploy/             Dockerfile, systemd user unit
├─ docs/
└─ go.mod
```

Go 1.23+. One statically linked binary `hat` (`CGO_ENABLED=0`), released for
`linux/amd64` and `linux/arm64`.

| Purpose | Library |
|---|---|
| CLI | `spf13/cobra` |
| D-Bus (notifications) | `godbus/dbus/v5` |
| SQLite | `modernc.org/sqlite` |
| YAML | `goccy/go-yaml` |
| JSON Schema validation | `santhosh-tekuri/jsonschema` |
| HTML | `a-h/templ` + HTMX |
| Translations | `nicksnyder/go-i18n/v2`, catalogs `internal/i18n/locales/{en,de}.yaml`, embedded |

**Language:** every user-visible text goes through the catalog; English is
the fallback for missing German strings. The agent and CLI resolve the
language from `language` in `agent.yaml`, then `LC_ALL`, `LC_MESSAGES`,
`LANG`. The server resolves it per request from a cookie, then
`Accept-Language`; MVP 1 has no switcher (it comes with the web UI in
[MVP 2](mvp-2.md)). Names of hats and activities are user data and are not
translated.

gsettings is called as a command (`gsettings set …`), which avoids a GLib
dependency and works the same on every distribution.

### Processes

```
 hat (CLI) ──JSON lines──► hat agent ──REST──► hat server ──► SQLite
                           │    ▲                │
                           │    └────── SSE ─────┘
                           └──► modules ──► gsettings, D-Bus, scripts
```

- The **CLI** is short-lived and only talks to the agent. If the agent is not
  running, the CLI says so and how to start it.
- The **agent** is the only process that touches the desktop or the network.
- The **server** holds the truth: configuration, event log, current state.

### Interfaces

**Agent socket:** `$XDG_RUNTIME_DIR/hatrack/agent.sock`, one JSON object per
line, request/response.

```json
{"cmd": "on", "hat": "ve", "activity": "rev", "at": null}
{"ok": true, "state": {"hat": "venom", "activity": "review", "since": "..."}}
```

**REST API** (bearer token per device):

| Method and path | Purpose |
|---|---|
| `POST /api/events` | append one or more events (idempotent by UUID) |
| `GET /api/state` | current state |
| `GET /api/events?from&to` | events of a period |
| `GET /api/intervals?from&to` | derived intervals and totals |
| `GET /api/config` / `PUT /api/config` | export / import YAML (`?dry_run`, `?replace`) |
| `GET /api/stream` | SSE: `state`, `config` |
| `POST /api/devices/hello` | register capabilities, last seen |

**Storage interface** in core: `append_events`, `events_between`,
`current_state`, `get_config`, `put_config`. MVP 1 implements it with SQLite;
the server and later the standalone agent use the same code.

**Module interface:**

```go
type Backend interface {
    Name() string
    Detect(ctx context.Context) bool
    Apply(ctx context.Context, cfg json.RawMessage) error
    Snapshot(ctx context.Context) (json.RawMessage, error)
}

type Module interface {
    ID() string
    RunsIn() RunsIn              // Agent or Core
    ConfigSchema() []byte        // JSON Schema
    Backends() []Backend
    Enter(ctx context.Context, sw Switch, cfg json.RawMessage) error
    Leave(ctx context.Context, sw Switch, cfg json.RawMessage) error
}
```

Built-in modules register themselves in a registry at startup. The agent
picks the first backend whose `Detect` returns true and reports the result to
the server.

**External modules** live in `~/.config/hatrack/modules/<id>/` with a
`module.yaml` (id, schema, hooks) and an executable. The agent calls it with
the hook name as argument and the switch plus configuration as JSON on stdin;
the answer (ok or error, optional snapshot) comes back as JSON on stdout.
Built-in and external modules look the same to the rest of the agent.

### Files

| Path | Content |
|---|---|
| `~/.config/hatrack/agent.yaml` | server URL, device name, token, language |
| `~/.local/state/hatrack/agent.db` | offline queue, neutral snapshot, cached config and state |
| server `/data/hatrack.db` | events, configuration, devices |

### Server deployment

MVP 1 needs a server: configuration lives there, and `hat config import`
goes through the agent to it. For daily use on one machine, run it locally on
`127.0.0.1`. The agent caches the configuration, so later server outages do
not stop switching.

`hat server` runs as a Docker container (a scratch image with only the
binary) on any host, or directly as a process, for example locally on
`127.0.0.1` during development. The SQLite file lives in `/data`;
`hat server backup` writes a consistent copy for backups. A device
is added with `hat server device add <name>`, which prints the token for
`hat agent login`.

### Tests

- Go's `testing` package, table-driven tests.
- Core: unit tests for name resolution, interval calculation (including
  `--at`, out-of-order and offline events), config import/export round trip.
- Agent: modules run against fake backends; socket protocol tests.
- Server: API tests with a temporary SQLite file.
- The Cinnamon backends are tested by hand on Bert's desktop.

## Done

Bert tests MVP 1 in daily use and decides when it is done.
