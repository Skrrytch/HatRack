# MVP 2: Web UI, tray and more desktops

MVP 2 builds on [MVP 1](mvp-1.md). The command line stays the base; the web
UI and the tray are the tools for every day. MVP 2 adds both, makes the log
editable, adds MCP, and brings HatRack to more devices and desktops.

MVP 2 is built in slices, each usable on its own:

1. **Web UI, read-only (done):** login, Now with switch bar, Day, Week, CSV.
2. **HatTray:** icon and menu for switching.
3. **Web UI, Setup:** hats, activities and module settings as forms.
4. **Corrections:** editable Day view.
5. **MCP, `launch`, `mqtt`, GNOME and KDE backends, several devices.**

## Capabilities

- **HatTray.** A tray icon in the colour of the current hat (grey when
  paused or none). Its menu lists the hats; each hat has its activities as a
  submenu. Further entries: pause/resume, "Open HatRack" (the web UI) and
  the current state as the first line. `hat tray run` starts it, `hat tray
  show` / `hide` / `toggle` control the icon from the command line or a
  shortcut. It follows switches made anywhere (CLI, web UI, other device).
- **Full web UI.** Now, Day (timeline, editable), Week (totals per hat and
  activity, CSV export), Setup (hats, activities, module settings) and
  Devices (capabilities, last seen, errors).
- **Edit the log.** Move an interval boundary, change hat or activity of an
  interval, insert or delete an interval. Every edit is a correction event,
  so history stays traceable.
- **AI access through MCP.** An AI can read the current state, hats,
  activities, intervals and events, switch hats, add notes and propose
  corrections. Proposed corrections wait for confirmation in the web UI.
- **Open apps and websites per hat.** The `launch` module starts applications,
  `.desktop` entries and URLs when a hat is put on.
- **Publish the state over MQTT.** The `mqtt` module (runs on the server)
  publishes the current hat, for example for Home Assistant.
- **More desktops.** `wallpaper` and `color_scheme` get backends for GNOME and
  KDE Plasma.
- **Several devices.** Device-level settings, capability reporting per device,
  and the Devices view make a second machine practical.
- **Ask through a notification.** `resume_activity: ask` works without a
  terminal: a notification offers "Resume review" and "No activity".
- **Configuration bundles.** `hat config export --bundle` writes a zip with the
  YAML and all referenced files (wallpapers, scripts); import unpacks them.

## Components

| Component | Where | New in MVP 2 |
|---|---|---|
| Core | `internal/core` | correction events, reports (totals, CSV), forms from JSON Schemas, MCP tool implementations, config bundles |
| Server | `internal/server` (`hat server`) | web UI (`templ` + HTMX), MCP endpoint `/mcp`, token scopes, runtime for `core` modules |
| Agent / CLI | `internal/agent`, `internal/cli` | `watch` and `hats` socket commands, notification actions, device settings layer, `config export --bundle` |
| HatTray | `internal/tray` (`hat tray`) | StatusNotifierItem and menu (`fyne.io/systray`), talks only to the agent socket |
| Module `launch` | built in | backend `xdg` (exec, `gtk-launch`/`.desktop`, `xdg-open`) |
| Module `mqtt` | built in, `runs_in: core` | publishes state to a broker |
| Backends | built in | `wallpaper` and `color_scheme` for `gnome` and `kde` |

## How it works

### Corrections

A correction is an event of type `correction` with:

```
supersedes: [event ids]     events that no longer count
replacement: [events]       events that count instead (may be empty)
source: user | ai
status: applied | proposed | rejected
reason: free text
```

Interval calculation first drops superseded events, then derives intervals
as in MVP 1. Every edit in the Day view becomes exactly one correction:

| Edit in the Day view | Correction |
|---|---|
| move a boundary | supersede the event at the boundary, replace with the same event at the new time |
| change hat/activity of an interval | supersede the opening event, replace with the changed one |
| insert an interval | no supersede; replace with a start and an end event |
| delete an interval | supersede the opening event; replace with an `off` or the previous hat |

Corrections with `source: ai` start as `proposed`. They appear in the Day
view and count only after confirmation.

### Web UI

- **Now:** current hat and activity, duration, a switch bar with all hats
  and activities, devices online.
- **Day:** timeline of intervals per day, inline editing (see above), pending
  AI proposals highlighted.
- **Week:** totals per hat and per hat/activity, comparison with the previous
  week, CSV export (`date, hat, activity, start, end, minutes, note`).
- **Setup:** hats and activities (add, rename, archive, sort), module settings
  per hat and default, forms generated from each module's JSON Schema, YAML
  import and export.
- **Devices:** name, last seen, detected backends per module, recent module
  errors, device-level settings.

The web UI is a session-based client with a login (one password, see
[decisions.md](decisions.md)). Switches from the browser are written by the
server with the same rules as the CLI (`core.PlanOn` and friends) and reach
all agents through SSE. A language switch (English/German) in the header overrides the
browser language and is stored in a cookie.

### MCP

| Tool | Kind | Purpose |
|---|---|---|
| `get_current` | read | hat, activity, since |
| `list_hats`, `list_activities` | read | master data |
| `query_time(from, to, group_by)` | read | totals by hat, activity, day |
| `list_events(from, to)` | read | raw events and intervals |
| `switch(hat, activity?)` | write | same rules as the CLI |
| `add_note(text, at?)` | write | note on the current or a past interval |
| `propose_correction(...)` | write | creates a `proposed` correction |

The MCP token has scopes: `read`, `switch`, `note`, `propose`. Without a write
scope the tools are not listed.

### `launch`

On `enter`, each entry is started unless it is already running (optional
check by process name or window class). `leave` does nothing; closing windows
is out of scope until window modules exist.

```yaml
launch:
  - app: org.mozilla.firefox.desktop
  - url: https://jira.example.com
  - cmd: "code ~/projects/venom"
    unless_running: code
```

### `mqtt`

Runs where the core runs. On every state change it publishes a retained JSON
message to `hatrack/<topic>/state`:

```json
{"hat": "venom", "name": "VENOM", "color": "#7a1fa2", "activity": "review",
 "since": "2026-11-04T09:12:00+01:00", "paused": false}
```

### Notification actions

For `resume_activity: ask` without a terminal, the agent sends a notification
with actions (D-Bus `ActionInvoked`). The switch happens immediately without
an activity; choosing "Resume review" adds an `activity` event with the
switch time, so no minute is lost.

### HatTray

The tray is a thin client on the agent socket, like the CLI.

- The socket gets two commands: `hats` (hats with colour, activities and the
  resume setting) and `watch`, which keeps the connection open and sends a
  line for every state or configuration change.
- A click on a hat sends `on <hat>`; a click on an activity sends
  `on <hat> do <activity>`, or `do <activity>` for the current hat.
  `resume_activity: ask` is handled by the same notification as in the
  section below, because the tray has no terminal.
- The icon is drawn in code (a hat silhouette in the hat's colour) and sent
  as a PNG, so no theme or toolkit is needed.
- `hat tray show|hide|toggle` talk to the running tray through its own small
  socket (`tray.sock`); `hat tray run` starts hidden or shown according to
  `tray.yaml` in the agent configuration.
- Cinnamon shows StatusNotifier icons through `xapp-sn-watcher` (Linux Mint).
  Where no watcher runs, `hat tray run` says so and exits. GNOME needs the
  AppIndicator extension; this is a hint in the message, not a dependency.

### Settings layers

Module settings resolve as default → hat → device. The device layer lives on
the server and is edited in the Devices view; an agent receives only its own
layer.

## Architecture

### Server

```
hat server
├─ /api        REST, as in MVP 1, plus /api/corrections
├─ /api/stream SSE
├─ /mcp        MCP, official Go SDK (modelcontextprotocol/go-sdk), Streamable HTTP
├─ /           web UI: templ components, HTMX partials, embedded in the binary
└─ core runtime for runs_in: core modules (mqtt)
```

- Web UI, API and MCP call the same core functions; there is no logic in the
  templates or the MCP layer.
- Pages are rendered with `html/template`; a few lines of JavaScript set the
  time zone cookie and reload the page when the state changes.
- The form renderer turns a JSON Schema into HTML inputs (string, number,
  boolean, enum, path, list of objects) and validates on the server.
- `core` modules subscribe to state changes inside the server process.
- `mqtt` uses `eclipse/paho.golang`.

### Auth

| Client | Credential | Scopes |
|---|---|---|
| agent | device token | events, config read, own device settings |
| web UI | user login, session cookie | everything |
| MCP | MCP token | as configured |

### Tests

- Tray: menu model (hats, activities, checked state) is a pure function of
  the `hats` and `status` answers and is unit-tested; the D-Bus part is
  tested by hand on Cinnamon.
- Core: correction semantics (supersede chains, proposed vs applied), report
  totals, schema-to-form rendering.
- Server: web routes with HTMX requests, MCP tools against a test database.
- Backends for GNOME and KDE: manual test on a VM or live session each.

## Done

Bert tests MVP 2 in daily use and decides when it is done.
