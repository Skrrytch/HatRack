# MVP 2: Web UI, tray and more desktops

MVP 2 builds on [MVP 1](mvp-1.md). The command line stays the base; the web
UI and the tray are the tools for every day. MVP 2 adds both, makes the log
editable, adds MCP, and brings HatRack to more devices and desktops.

MVP 2 is built in slices, each usable on its own:

1. **Web UI, read-only (done):** login, Now with switch bar, Day, Week, CSV.
2. **HatTray (done):** icon and menu for switching.
2b. **Panel (done):** the quick switcher, opened by left click on the tray icon or a keyboard shortcut.
3. **Web UI, Setup (done):** hats, activities and module settings as forms, YAML import and export.
3b. **Web UI, daily use (done):** Now in the panel's style (status in the hat colour, Pause and Take off as in the panel, live duration, today per hat, edit link on every hat), a new hat with its activities in one step, several activities at once, module sections that say what applies, devices and YAML moved down in Setup, a narrow layout for phones.
4. **Corrections (done):** editable Day view: change, delete, fill gaps, add entries, undo.
5. **MCP, `launch`, `mqtt`, GNOME and KDE backends, several devices.** (`mqtt` was built in [MVP 5](mvp-5.md).)
6. **Details (done):** an optional free text below the activity ("VENOM · review · PR 482"), with Jira keys as learned links.

## Capabilities

- **HatTray.** A tray icon in the colour of the current hat (grey when
  paused or none). A left click opens the panel (below). The right-click menu is
  slim: the current state as the first line, pause/resume, "Open HatRack"
  (the web UI), "About HatRack" and "Quit tray". `hat tray run` starts it, `hat tray
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
| Module `launch` (done) | built in | backend `xdg`: programs by `.desktop` id with `gtk-launch`, addresses, folders and files with `xdg-open`; on putting a hat on only, nothing is closed |
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
  import and export. Done as `/setup` (hat list, archived hats, defaults,
  YAML with preview), `/setup/hat?key=…` and `/setup/defaults`. Every change
  goes through the same merge and checks as `hat config import`, and the
  agents get it at once over SSE. Empty fields on a hat fall back to the
  defaults, which the form shows in grey. Buttons for single activities
  submit the whole form, so nothing typed is lost; Enter saves.
- **Devices:** name, last seen, detected backends per module, recent module
  errors, device-level settings.

The web UI is a session-based client with a login (one password, see
[decisions.md](decisions.md)). Switches from the browser are written by the
server with the same rules as the CLI (`core.PlanOn` and friends) and reach
all agents through SSE. A language switch (English/German) in the header overrides the
browser language and is stored in a cookie.

### Details

Hats and activities stay fixed categories. A **detail** is a third, free and
optional level: one line of text, never required, never set up beforehand.

> **VENOM · review · PR 482** (hat · activity · detail)

**Behaviour**

- The first detail of a running entry applies to the whole entry, from its
  start: it can be added after 20 minutes without splitting anything.
- Changing a detail starts a new entry from now, like changing the activity.
  Changing the activity or the hat starts without a detail (unless one is
  given with it). Pause and resume keep the detail.
- Time per detail can then be told apart: "review 3h, of which PR 482
  1h 40m".

**Entering it, quickly and anywhere**

| Where | How |
|---|---|
| Panel | Typing "rev PR 482" + Enter: the first word picks the activity (prefix), the rest is the detail; without a rest nothing changes. The recent details of the worn activity are chips under it, one click each. |
| CLI | `hat do review "PR 482"`, `hat on venom do review: PR 482`, `hat detail "PR 482"` for the current activity |
| Web, Now | A quiet "+ Detail" below hat and activity in the head; a click turns it into a field, Enter saves; the recent details as chips |
| Web, Day | The detail is part of each entry's edit form |
| Tray menu | nothing, it stays slim |

Suggestions are the last five details per hat and activity of the last two
weeks; what is not used for a while disappears by itself.

**Model**

- `switch` and `activity` events carry a field `detail`. A changed detail is
  an `activity` event with the same activity (a new entry). The first detail
  of a running entry is an event of the new type `detail`, which relabels
  the running interval from its start. This stays append-only and works
  offline in the agent, without corrections. Corrections fold `detail`
  events into the event that opened their entry, so moving boundaries never
  carries a detail into a neighbour.
- `State` and `Interval` carry the detail; the Day view's edits keep or change
  it. The events table gets a column `detail` (migration).

**Jira keys as learned links** (as in Ruumble)

- A detail with an issue link (`https://jira.example/browse/TAG-1366`, Jira
  Cloud and Data Center alike) teaches the server where project `TAG` lives
  (`https://jira.example/browse/`). From then on a plain "TAG-1366" in any
  detail links there. Only the shape of the URL counts, nothing is fetched,
  Jira needs no configuration.
- The detail is stored as typed (lossless, works offline); everywhere it is
  shown as the key only ("TAG-1366"), linked if the project is known: web UI,
  panel and CLI (key only, no link), CSV, MCP (`detail` plus `detail_url`).
- The oldest link wins, so a later link cannot redirect a known project.
  Setup lists the learned projects (project → address) with a reset each,
  e.g. after a first link to the wrong Jira; the next link teaches it again.
- Keys: an uppercase letter and 1–9 uppercase letters or digits, a dash, a
  number (`TAG-1366`, `VKB2-17`); not inside words, paths or longer keys.

**Evaluation**

- Day and Week: each activity in the totals unfolds into its details.
- CSV: the column `note` holds the detail (short form).
- MCP: `list_entries` returns `detail` and `detail_url`; `query_time` gets
  `group_by: detail` (hat, activity and detail).

### MCP

An AI assistant reads HatRack's configuration and, above all, the time log:
"how long did I work for VENOM last week, split by activity?" The first step
is read-only; Bert uses Claude Code, which talks to `/mcp` over HTTP, so no
local bridge is needed.

**Step 1 (done):**

1. **Access:** MCP tokens, managed in Setup → "AI access": name ("Claude Code
   laptop"), shown once, only the hash stored (table `mcp_tokens`: id, name,
   hash, scopes, created, last_used), revocable at once. The only scope for
   now is `read`; the column exists so that later scopes need no migration.
2. **`/mcp`:** Streamable HTTP with the official Go SDK
   (`github.com/modelcontextprotocol/go-sdk`), `Authorization: Bearer <mcp
   token>`. Device tokens and web sessions do not work there, and MCP tokens
   work nowhere else. Last use is shown in Setup.
3. **Read tools** (times first, they are the main purpose):

| Tool | Purpose |
|---|---|
| `query_time(from, to, group_by, hat?)` | totals by `hat`, `activity`, `day` or `week`, optionally for one hat; with each day's working hours and the share outside them |
| `list_entries(from, to, hat?)` | the entries (intervals) with hat, activity, start, end, duration; gaps marked |
| `get_current` | hat, activity, since, duration, today per hat |
| `list_hats(include_archived?)` | hats with key, name, colour, activities, resume setting |
| `get_config` | the whole configuration as YAML, as `hat config export` gives it |

- Periods: dates (`2026-11-04`) or RFC 3339 times, and the words of
  `hat log` (`today`, `yesterday`, `week`) plus `last week`, `month` and
  `last month`, in the server's time zone. Hats resolve like on the command line ("ven" finds
  VENOM). Corrections count, proposals do not.
- Answers are JSON (structured content) plus a short text summary, so the
  assistant can both compute and quote.
- Errors are plain sentences the assistant can pass on.

**Connecting Claude Code:**

```
claude mcp add --scope user --transport http hatrack https://<server>/mcp --header "Authorization: Bearer <token>"
```

The Setup page shows this line, filled in, when a token is created
(`--scope user`: available in every project, not only the current folder).

**Tests:** the SDK's client runs every tool against an `httptest` server:
auth (no token, device token, revoked token), each tool, periods, prefix
names, corrections counted, bad input. No real assistant is involved.

**Later (not planned yet):** a stdio bridge `hat mcp` for clients that start
a local command (Claude Desktop); write scopes `switch` (switch, pause, take
off, resume), `propose` (corrections that wait for confirmation in the Day
view, stored as the intended edit and planned on accept) and `note`.

### `launch`

Done. On putting a hat on (a look change, so pausing and resuming does not
open things twice), `apps` are started by their `.desktop` id with
`gtk-launch` and `open` entries (addresses, folders, files) with `xdg-open`,
detached from the agent. Missing programs or paths are reported as module
errors. Nothing is closed when the hat comes off.

```yaml
launch:
  apps: [firefox, org.gnome.Nautilus]
  open: [https://jira.example.com, ~/Projekte/venom]
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
- The icon is drawn in code (a top hat in the hat's colour, grey while
  paused or without a hat) and sent as a PNG, so no theme or toolkit is
  needed. The tooltip shows the status line.
- The library has no way to hide the icon of a running process on Linux, so
  `hat tray hide` ends the tray process (over its own small socket,
  `tray.sock`), `hat tray show` starts one in the background and `hat tray
  toggle` does whichever applies. `hat tray run` runs in the foreground; for
  start at login run `hat tray install` (writes `~/.config/autostart/hatrack-tray.desktop`);
  `hat tray restart` restarts a running tray after an update.
- Cinnamon shows StatusNotifier icons through `xapp-sn-watcher` (Linux Mint).
  Where no watcher runs, `hat tray run` says so and exits. GNOME needs the
  AppIndicator extension; this is a hint in the message, not a dependency.

### Panel

The quick switcher: one small window with every hat as a card and its
activities as chips, an icon toolbar (pause/resume, take the hat off and
restore the normal desktop look, open HatRack; About is in the tray's
right-click menu) and a hint line that says what a click on the hovered item does.

- Opened by a left click on the tray icon (next to the pointer) or by
  `hat panel` (middle of the monitor with the pointer; `--pointer` puts it at
  the pointer). Bind `hat panel` to a keyboard shortcut in the desktop's
  settings. A running tray shows it instantly; without a tray `hat panel`
  draws it itself.
- Closes with Escape or a click outside. One click on a hat or an activity
  puts it on and closes the panel. Typing filters hats and activities by
  prefix; arrow keys, Tab and Enter work too.
- It is plain Go: layout and drawing (embedded Go fonts, software
  rendering) are independent of the window system; the window itself is an
  X11 backend (`github.com/jezek/xgb`, override-redirect window with
  keyboard and pointer grab, size from `Xft.dpi`, monitor from RandR). A
  Wayland backend can be added later. Without X11 the panel opens the web UI.
- Tested with Xvfb, xdotool and ImageMagick (skipped where they are not
  installed); never against the real display.

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

- Tray: the menu model (hats, activities, checked state) is a pure function
  of the agent's snapshot and is unit-tested, as are the icon and the
  `hats`/`watch` socket commands. The D-Bus part was checked on Cinnamon
  with `xapp-sn-watcher`: the item registers, the menu layout is correct and
  a click through `com.canonical.dbusmenu` switches the hat.
- Core: correction semantics (supersede chains, proposed vs applied), report
  totals, schema-to-form rendering.
- Server: web routes with HTMX requests, MCP tools against a test database.
- Backends for GNOME and KDE: manual test on a VM or live session each.

## Done

Bert tests MVP 2 in daily use and decides when it is done.
