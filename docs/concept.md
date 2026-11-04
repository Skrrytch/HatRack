# HatRack concept

## Terms

| Term | Meaning |
|---|---|
| **Hat** | a context such as Office, VENOM, TAG, Private. Exactly one hat is on, or none (pause). |
| **Activity** | what you do while wearing a hat. Each hat has its own list (VENOM: development, review, meeting …). |
| **Switch** | putting on a hat: leave the old one, apply the new one, write the log. |
| **Module** | one capability that reacts to switches (wallpaper, colour scheme, launch, mqtt …). |
| **Backend** | the implementation of a module for one desktop or environment. |
| **Device** | a machine running the agent. |

## Architecture

```
 Server (optional, e.g. Docker)               Devices (laptop, PC …)
┌──────────────────────────────┐             ┌───────────────────────────────┐
│ hatrack-server               │  REST + SSE │ hat agent (user service, no UI)│
│  ├─ API          (/api) ◄────┼─────────────┤  ├─ module runtime            │
│  ├─ web UI       (/)         │             │  ├─ offline queue             │
│  ├─ MCP          (/mcp) ◄─ AI│             │  └─ local socket              │
│  ├─ core modules (mqtt …)    │             │ hat (CLI)    ─┐                │
│  └─ SQLite: event log        │             │ HatTray (Qt) ─┴─► local socket │
└──────────────────────────────┘             └───────────────────────────────┘
```

- **The core is a library**, not a server. Data model, event log, time
  calculation, module runtime, schema-driven forms, views and MCP tools live in
  `hatrack-core`. The server and the agent are thin shells around it.
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
│             hooks: enter, leave, activity_change, idle
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
6. Modules are Python packages found via entry points. A folder is enough for
   personal modules.
7. A **neutral** state is saved on first start and can always be restored.

### Portability notes

| Feature | Portable | Per desktop |
|---|---|---|
| Light/dark, wallpaper | – | gsettings (GNOME/Cinnamon/MATE), KDE tools, xfconf, swww/hyprpaper |
| Notifications | D-Bus notifications | – |
| Launch apps/URLs | exec, xdg-open, .desktop files | – |
| Move/close windows | – | X11 wmctrl, KWin scripts, sway/hyprland IPC; GNOME Wayland hardly possible |
| Tray | StatusNotifierItem via Qt | GNOME needs the AppIndicator extension |
| Global shortcuts | bind `hat …` in the desktop's own shortcut settings | XDG GlobalShortcuts portal later |
| Idle/lock | logind partly | screensaver D-Bus per desktop |

## Data model

```
hats(id, name, color, sort, archived, rev, updated_at)
activities(id, hat_id, name, archived, rev, updated_at)
module_settings(module, hat_id?, device_id?, json, rev, updated_at)
devices(id, name, token_hash, capabilities_json, last_seen)
events(id uuid, ts, device_id, type, hat_id, activity_id, note, payload_json)
```

- Event types: `switch`, `activity`, `off`, `idle_start`, `idle_end`,
  `correction`, `module_error`.
- Events are append-only. Intervals and reports are derived.
- A correction is a new event that supersedes an old one, so every change,
  including one proposed by an AI, stays traceable.
- UUIDs and device timestamps make queued events and a later
  standalone → server migration safe. `rev`/`updated_at` on master data prepare
  for syncing.

## Server

| Part | MVP choice |
|---|---|
| Language | Python, FastAPI |
| Storage | SQLite behind a storage interface |
| Web UI | server-rendered (Jinja + HTMX), forms from JSON Schemas |
| MCP | official Python SDK, Streamable HTTP at `/mcp` (stdio in standalone mode) |
| Auth | one token per device, one for MCP; write tools only with permission |

**Views:** Now (current hat, activity, duration, devices online) · Day
(timeline, editable) · Week (totals per hat and activity, CSV export) · Setup
(hats, activities, module settings) · Devices (capabilities, last seen, errors).

**MCP tools:** read `get_current`, `list_hats`, `list_activities`,
`query_time(from, to, group_by)`, `list_events`; write `switch`, `add_note`,
`propose_correction` (corrections by an AI need confirmation in the web UI).

## Client

- **`hat agent`:** `systemd --user` service, installed with `pipx`/`uv tool`.
  Holds the SSE connection, runs modules, queues offline events.
- **`hat`:** `on`, `do`, `off`, `status`, `log`, `menu`. Talks to the agent over
  a Unix socket.
- **HatTray:** small Qt app (StatusNotifierItem), icon in the hat's colour,
  menu with hats and their activities. Optional.

## MVP scope

**In:** server with API, SSE, SQLite, the five views and MCP (read + `switch`);
agent with offline queue, CLI and tray; modules `wallpaper` (gnome/cinnamon,
kde), `color_scheme` (gnome/cinnamon, kde), `notify`, `launch`, `exec`, `mqtt`
(core); activities per hat.

**Out, for now:** moving/closing windows, idle detection (comes right after),
suggestions from the active window, multiple users, public access, standalone
mode (planned as the next milestone; the MVP must keep the structure ready).

## Milestones

1. `hatrack-core`: data model, event log, time calculation, module runtime;
   usable and testable locally.
2. `hatrack-server` and `hat agent` on top of it; HatTray.
3. Standalone mode.
4. Idle detection, daily summary.
5. Suggestions from the active window, window modules, more backends.

## Later ideas

Firefox profile per hat, git identity per hat (`includeIf gitdir`), SSH keys
and VPN, focus mode, pause reminders and Pomodoro, calendar as activity
suggestions, Home Assistant scenes via MQTT, export to Kimai.
