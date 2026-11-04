# HatRack concept

## Terms

| Term | Meaning |
|---|---|
| **Hat** | a context such as Office, VENOM, TAG, Private. At most one hat is worn at a time; when paused, the hat stays on the rack with its look but the clock stops. |
| **Activity** | what you do while wearing a hat. Each hat has its own list (VENOM: development, review, meeting …). |
| **Switch** | putting on a hat: leave the old one, apply the new one, write the log. |
| **Module** | one capability that reacts to switches (wallpaper, colour scheme, launch, mqtt …). |
| **Backend** | the implementation of a module for one desktop or environment. |
| **Device** | a machine running the agent. |

## Architecture

```
 Server (optional, e.g. Docker)               Devices (laptop, PC …)
┌──────────────────────────────┐             ┌───────────────────────────────┐
│ hat server                   │  REST + SSE │ hat agent (user service)      │
│  ├─ API          (/api) ◄────┼─────────────┤  ├─ module runtime            │
│  ├─ web UI       (/)         │             │  ├─ offline queue             │
│  ├─ MCP          (/mcp) ◄─ AI│             │  └─ local socket              │
│  ├─ core modules (mqtt …)    │             │ hat (CLI)    ─┐               │
│  └─ SQLite: event log        │             │ hat tray     ─┴─► local socket│
└──────────────────────────────┘             └───────────────────────────────┘
```

- **One program, one binary.** `hat` contains the CLI, the agent, the server
  and the tray as subcommands (`hat agent`, `hat server`, `hat tray run`).
- **The core is a library**, not a server. Data model, event log, time
  calculation, module runtime, schema-driven forms, views and MCP tools live in
  the core packages. The server and the agent are thin shells around it.
- **Connected mode:** the server holds the truth; agents apply what it says and
  report what their device can do. A switch can come from anywhere (tray, CLI,
  web UI, MCP) and is pushed to all agents over SSE.
- **Standalone mode (later):** the agent runs the core itself with a local
  SQLite file, serves the web UI on `127.0.0.1` and MCP over stdio.
- **Offline:** if the server is unreachable, the agent switches anyway and
  queues the events with the device's timestamp.
- **CLI and tray only talk to the local agent**, never to the server. Any UI
  (tray, a Cinnamon applet, a KDE widget, rofi) is a thin client on the socket.

## Modules

```
module "wallpaper"
├─ manifest   id, version, runs_in: agent | core
│             config_schema (JSON Schema) → forms in the web UI
│             hooks: enter, leave (idle later)
└─ backends   gnome · cinnamon · kde · xfce · sway · hyprland …
              each: detect() → bool, apply(cfg), snapshot()
```

1. **Capabilities, not assumptions.** On start the agent calls `detect()` on
   every backend and reports the result. The web UI shows per device what works.
2. **No backend, no feature.** A missing or failing module is logged and
   reported; the switch continues.
3. **`runs_in`:** `agent` modules touch the device (wallpaper). `core` modules
   run wherever the core runs (mqtt): on the server, or in the agent in
   standalone mode.
4. **Settings in three layers:** module default → hat → device.
5. **`exec` as the universal fallback:** run any script on enter/leave.
6. Built-in modules are compiled into `hat`. External modules are executables
   in a folder that exchange JSON over stdin/stdout, so a folder is enough for
   personal modules, written in any language.
7. A **neutral** state is saved on first start and can always be restored.

### Portability notes

| Feature | Portable | Per desktop |
|---|---|---|
| Light/dark, wallpaper | – | gsettings (GNOME/Cinnamon/MATE), KDE tools, xfconf, swww/hyprpaper |
| Notifications | D-Bus notifications | – |
| Launch apps/URLs | exec, xdg-open, .desktop files | – |
| Move/close windows | – | X11 wmctrl, KWin scripts, sway/hyprland IPC; GNOME Wayland hardly possible |
| Tray | StatusNotifierItem over D-Bus | GNOME needs the AppIndicator extension |
| Global shortcuts | bind `hat …` in the desktop's own shortcut settings | XDG GlobalShortcuts portal later |
| Idle/lock | logind partly | screensaver D-Bus per desktop |

## Data model

```
hats(id, key, name, color, resume_activity, sort, archived, rev, updated_at)
activities(id, hat_id, name, sort, archived, rev, updated_at)
module_settings(module, hat_id?, device_id?, json, rev, updated_at)
devices(id, name, token_hash, capabilities_json, last_seen)
events(id uuid, ts, device_id, type, hat_id, activity_id, note, payload_json)
```

- Event types: `switch`, `activity`, `off`, `idle_start`, `idle_end`,
  `correction`, `module_error`.
- `key` is the stable identifier used in the YAML configuration; `name` is
  the display name. Activities are identified by name within their hat.
- Events are append-only. Intervals and reports are derived.
- A correction is a new event that supersedes an old one, so every change,
  including one proposed by an AI, stays traceable.
- UUIDs and device timestamps make queued events and a later
  standalone → server migration safe. `rev`/`updated_at` on master data prepare
  for syncing.

## Server

| Part | MVP choice |
|---|---|
| Language | Go, standard library `net/http` |
| Storage | SQLite (`modernc.org/sqlite`, pure Go) behind a storage interface |
| Web UI | server-rendered (MVP 1: `html/template`; from MVP 2 `templ` + HTMX, embedded in the binary), forms from JSON Schemas |
| MCP | official Go SDK, Streamable HTTP at `/mcp` (stdio in standalone mode) |
| Auth | one token per device, one for MCP; write tools only with permission |

**Views:** Now (current hat, activity, duration, devices online) · Day
(timeline, editable) · Week (totals per hat and activity, CSV export) · Setup
(hats, activities, module settings) · Devices (capabilities, last seen, errors).

**MCP tools:** read `get_current`, `list_hats`, `list_activities`,
`query_time(from, to, group_by)`, `list_events`; write `switch`, `add_note`,
`propose_correction` (corrections by an AI need confirmation in the web UI).

## Client

- **`hat agent`:** `systemd --user` service. Installation is copying one
  binary. Holds the SSE connection, runs modules, queues offline events.
- **`hat`:** `on`, `do`, `off`, `status`, `log`, `config`. Talks to the agent
  over a Unix socket.
- **HatTray (`hat tray`):** StatusNotifierItem over D-Bus (`fyne.io/systray`,
  no GTK or Qt), icon in the hat's colour, menu with hats and their
  activities. Optional.

## Stages

The MVP is split into stages. Each document starts with the capabilities,
then the components, then how it works and the architecture.

1. [MVP 1](mvp-1.md): core, server with API and SSE, agent, CLI, config as
   YAML, Cinnamon backends, `notify`, `exec`.
2. [MVP 2](mvp-2.md): web UI (Now, Day, Week, Setup) with corrections,
   HatTray, MCP, `launch`, `mqtt`, GNOME and KDE backends, several devices.
3. [MVP 3 and outlook](mvp-3.md): idle detection, export, standalone mode,
   then everything else sorted by benefit.

## Later ideas

Collected and sorted by benefit in [MVP 3 and outlook](mvp-3.md).
