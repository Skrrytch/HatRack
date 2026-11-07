# Epic: Automatic detection

HatRack learns to tell what you are working on: which hat, which activity,
which detail. It does so live, while you work, and afterwards, for time you
did not book. This document is the plan for all of it. It ties together the
GitHub issues of the epic and splits the work into five stages (MVP 7 to
MVP 11), each usable on its own.

## Goal

Today every entry in the log comes from a click, a command or a correction.
After this epic:

- **Live:** HatRack notices when you work on something other than the worn
  hat and offers to switch, or switches by itself where you allowed it
  (a meeting starts, the microphone is on).
- **Afterwards:** for any stretch of the day HatRack can say what it observed
  and what that most likely was, and turns that into proposals for the log.
  Gaps and forgotten switches are filled with a few keystrokes.
- **Absence:** time away from the computer is no longer counted by mistake.
- **For an AI:** the same prepared information is readable over MCP, so an
  assistant can summarise a week or propose corrections.

What it is not: HatRack does not try to profile behaviour. It records
*what was on the screen and which devices were in use*, condensed and
explainable, so the user (and an AI they invite) can evaluate it easily. It
never logs keystrokes, takes screenshots or reads content.

## Principles

1. **Observations are evidence, the log is a decision.** What sensors see is
   stored apart from the event log. The log keeps holding only what the user
   decided, or what a rule they enabled decided for them. Removing every
   observation leaves the log untouched.
2. **Propose, don't write.** Heuristics produce proposals. A proposal enters
   the log only when accepted, as a normal `correction` event. The one
   exception is a rule with `mode: auto`, and that switch carries its rule in
   the event, so it stays traceable and can be undone.
3. **Every proposal explains itself.** "VENOM / Entwicklung, 82 %, rule
   *VS Code in venom*" instead of a bare guess. Confidence is shown, not
   hidden.
4. **Ask rarely, ask well.** HatRack asks only where it is unsure and the time
   is not booked, prefills the answer, and can be answered with the keyboard.
5. **Private by default.** Sensors are off until enabled per device (idle is
   the exception). Lists of apps that are never recorded, titles that are cut
   down to the app name, a retention period. Nothing from observations goes
   to MQTT. Notification text never leaves memory.
6. **Same rules everywhere.** Sensors are agent modules with per-desktop
   backends and `detect()`; classification lives in the core, so it runs in
   the agent (live, offline) and in the server (afterwards) with the same
   result. No dependency on one desktop or display server.

## Pipeline

```
 Device (agent)                                 Core (agent live · server afterwards)
┌──────────────────────────────┐              ┌──────────────────────────────────────┐
│ Sensors                      │  segments    │ Classifier                           │
│  window  · idle/lock         ├─────────────►│  rules (config) + learned details    │
│  media (mic/cam)             │  (filtered,  │  → candidate per segment             │
│  notifications (RAM only)    │   queued)    │     hat · activity · detail · conf.  │
└──────────────┬───────────────┘              └───────┬──────────────────┬───────────┘
               │ live                                 │                  │
               ▼                                      ▼                  ▼
      ┌──────────────────┐              ┌──────────────────────┐  ┌───────────────┐
      │ Live: suggest or │              │ Reconstruction       │  │ MCP           │
      │ auto-switch,     │              │ smoothing · blocks · │  │ observations, │
      │ return from idle │              │ scores → proposals   │  │ blocks,       │
      └──────────────────┘              └──────────┬───────────┘  │ propose       │
                                                   ▼              └───────────────┘
                                  Review: Day view · panel prompt · end of day
                                                   ▼
                                       correction event (log)
```

### 1. Sensors (agent)

A sensor is a new kind of agent module: it has backends with `detect()` like
every module, but instead of `enter`/`leave` hooks it emits observations
while the agent runs. The device reports which sensors work, as it reports
its other capabilities today.

| Sensor | Observes | Backends (first ones in bold) |
|---|---|---|
| `window` | focused window: app (`WM_CLASS` or app id), title | **X11 via `xgb`** (`_NET_ACTIVE_WINDOW`, `_NET_WM_NAME`, `WM_CLASS`; event-driven through `PropertyNotify`); GNOME Wayland via a small Shell extension that `hat setup` installs; KWin script over D-Bus; sway and Hyprland IPC |
| `idle` | inactive since, screen locked | **logind `LockedHint`**, **`org.freedesktop.ScreenSaver`/Cinnamon/GNOME screensaver signals**, **X11 MIT-SCREEN-SAVER**, Mutter `IdleMonitor`, KDE `GetSessionIdleTime`; wlroots `ext-idle-notify` later |
| `media` | microphone capture stream active, camera open, and by which app | PipeWire (`pw-dump` JSON), PulseAudio (`pactl -f json`); camera: own processes holding `/dev/video*` (read from `/proc`, no root) |
| `notify` | desktop notifications matching a rule | D-Bus monitor on `org.freedesktop.Notifications.Notify`; text stays in memory, only a rule's result becomes an observation |

Explicitly not sensors: keyboard or mouse content, screenshots, browser
history, file contents, `/dev/input`.

### 2. Observations

An observation is a span with constant attributes:

```
observations(id, device_id, sensor, start, end, attrs_json, rev)
  window : {app, title}                       (title may be redacted)
  idle   : {kind: inactive | locked}
  media  : {mic: bool, cam: bool, app}
  notify : {rule, value}                      (e.g. value "Sprint Planning")
```

- The agent coalesces: a new span starts only when the attributes change. A
  workday is a few hundred to about two thousand spans.
- **Before anything leaves the device** the device's privacy settings apply:
  `ignore` apps are dropped (only "something private" with its length
  remains), `redact` apps keep only the app name, and while a hat with
  `observe: false` is worn only `idle` is recorded.
- Spans go to the server in batches (`POST /api/observations`) through the
  existing offline queue. The server keeps them for `retention` days
  (default 28; titles are sensitive, and four weeks cover any review) and
  then deletes them.
- Observations are not events: no corrections, no `rev` games, no export
  with the configuration.

### 3. Classifier (core)

The classifier turns a span into a **candidate**: hat, activity (optional),
detail (optional) and a confidence between 0 and 1. Two sources:

**Rules** in the configuration, so they move with export and import and are
edited in Setup on the hat's page:

```yaml
hats:
  - key: venom
    rules:
      - match: {app: code, title: "(?i)venom"}
        activity: Entwicklung
      - match: {title: "\\b(VEN-[0-9]+)\\b"}
        detail: "$1"
      - match: {media: mic, app: "(?i)teams|zoom"}
        activity: Meeting
        mode: auto                # suggest (default) | auto | off
defaults:
  rules:
    - match: {app: "(?i)spotify|keepassxc"}
      ignore: true                # noise: never a candidate
```

- `match` fields are regular expressions on the span's attributes (`app`,
  `title`, `notification`) or flags (`media: mic | cam`). All given fields
  must match.
- The hat is the one the rule is defined in; `activity` and `detail` are
  optional, `detail` may use capture groups.
- Several rules may fill different fields (one gives the activity, another
  the ticket key). For the same field: higher `priority` wins, then the rule
  with more conditions, then the earlier one.
- Rule matches have high confidence (0.9).

**Learned details** without any configuration: a title that contains a
detail used in the last eight weeks (ticket key, branch name) suggests the
hat and activity it was used with; a ticket key of a known project
(`core.TicketLinks`) suggests the hat that project was booked on. Medium
confidence (0.6). This makes the feature useful before the first rule is
written.

A span no rule or detail matches has no candidate ("unknown").

### 4. Live behaviour (agent)

The agent classifies spans as they happen, against the cached configuration,
so it works offline.

- **Dwell and hysteresis:** a candidate counts only after it held for a while
  (default 3 minutes of active time, idle excluded); short visits never
  trigger anything.
- **Manual wins:** after a manual switch, nothing is suggested or switched
  for 15 minutes unless a rule with `mode: auto` and a hardware signal
  (microphone) fires.
- **`suggest`:** a notification with an action ("Wechseln zu VENOM ·
  Entwicklung · VEN-142"), and a hint on the tray icon. Ignoring it is fine.
- **`auto`:** the switch happens; the event carries `source: rule:<id>`, a
  notification offers "Rückgängig". When the condition ends (microphone off)
  the previous hat and activity come back, also with `source`.
- **`auto` also starts a hat** when none is worn or the clock is paused: the
  most automatic behaviour is the default. Where a choice makes sense, a
  rule can later get `start: ask` (offer instead of start); not in the
  first version.

### 5. Idle and return

Idle is the base of everything else: idle time is subtracted before any
score is computed.

- `idle.after` (default 5 min) marks inactivity; a lock counts at once.
- Locked or inactive longer than `idle.stop_after` (default 15 min): the
  agent stops the clock backdated to the start of the absence (`off` with
  `--at`), so a computer locked overnight is not a working night.
- **Return:** for an absence longer than `idle.ask_after` (default 10 min)
  the panel asks, with the keyboard: "Weg 14:02–14:47 (45 min). Pause ·
  Weiter als VENOM · Meeting · anderes …". Each answer becomes exactly one
  correction (or nothing for "pause" when the clock was already stopped),
  then the hat resumes. Without the panel (Wayland for now) a notification
  with actions does the same; an unanswered absence stays a gap and shows up
  in the review.

### 6. Reconstruction (core, afterwards)

For any period the core rebuilds what happened:

1. **Load** the classified spans; cut them at the period's edges; drop idle.
2. **Smooth:** spans shorter than the noise threshold (default 60 s or 5 % of
   a block) and `ignore` spans hand their time to their neighbours, as
   `core.Settle` does for short entries. What remains are runs with one
   candidate each, with exact boundaries.
3. **Blocks:** the period is cut into blocks (default 15 min). Each block gets
   a score list: share of active time per candidate, unknown time as its own
   line, so the list always adds up to 100 % of the active time. Example:
   `[{VENOM/Entwicklung 53 %}, {Office/Kommunikation 47 %}]`.
4. **Proposals** come from the smoothed runs, not from the blocks. The log
   allows one hat at a time (decision 2026-11-04), so a block with two topics
   is booked as two entries in the order they happened, not as two shares of
   the same time. Blocks are the unit for reading, scoring and asking.
5. Proposals are made for **gaps** (unbooked working time) and for
   **disagreements** (the log says Office, 80 % of the observed time says
   VENOM). Each carries its confidence and the rules and details behind it.

Reconstruction is computed on demand and never stored, so a changed rule
changes the proposals for past days at once. It works only as far back as
observations are kept.

### 7. Review and asking

- **Day view:** below the logged band an "observed" band shows the runs in
  their hat colours (hatched when unsure, grey when unknown). Proposals appear
  as ghost entries with Accept / Reject; "Accept all gaps" for a whole day.
  Accepting writes one `correction` with `source: suggestion`.
- **Smart prompts:** instead of a fixed hourly question, the agent asks when
  at least `prompt.min_open` (default 20 min) of working time since the last
  question is unbooked or unsure, at most every `prompt.interval` (default
  60 min), never while the microphone is on. The notification opens the
  panel in review mode: one line per block, prefilled with the best
  candidate, Enter accepts, typing searches hats, activities and details as
  the detail field does today.
- **End of day:** one prompt for what is still open, together with the daily
  summary planned in [MVP 3](mvp-3.md).

### 8. MCP

The read-only MCP server grows by three tools:

| Tool | Scope | Returns |
|---|---|---|
| `summarize_blocks(from, to, block)` | `read` | blocks with their score lists and the logged entry, no titles |
| `list_observations(from, to, sensor)` | `observe` (new) | the spans with app and (redacted) title |
| `propose_correction(...)` | `propose` (new) | stores a correction with `status: proposed`, `source: ai`; it counts only after confirmation in the Day view |

`propose_correction` was planned since MVP 2; the review UI of MVP 9 is where
it gets confirmed. Titles are behind a scope of their own because they are
the most sensitive part.

## Settings

Following the three layers (default → hat → device):

| Where | Setting |
|---|---|
| defaults | `rules` (ignore rules), `idle.*`, `prompt.*`, `reconstruct.block`, `reconstruct.noise` |
| hat | `rules`, `observe: true \| false` |
| device | sensors on or off (`window`, `media`, `notify`; `idle` on by default), `ignore` and `redact` app lists |
| server | `observations.retention` (days, default 28) |

Setup gets a page "Erkennung" per device and a rule list on each hat's page
with a **rule test**: the last day's spans, which rule each one matches, and
what a new rule would match before it is saved.

## Stages

Each stage is usable on its own. The order follows dependencies: nothing
works without observations, nothing afterwards without rules.

| Stage | Name | What you get | Issues |
|---|---|---|---|
| **MVP 7** | Observe | Idle and lock handled, return question; window titles recorded on X11; an "observed" band in the Day view and the raw spans over MCP. Booking stays manual, but you can see what you did. | #6, #2, #1, #7 |
| **MVP 8** | Assign | Rules per hat, learned details, rule test in Setup; live suggestions and opt-in auto-switch. | #8, #9 |
| **MVP 9** | Afterwards | Reconstruction with blocks and proposals, review in the Day view, smart prompts, MCP block summary and AI proposals. | #4, #10, #3, #11 |
| **MVP 10** | Meetings | Microphone and camera as signals, meeting rules shipped as examples, meeting names from reminder notifications. | #5, #12 |
| **MVP 11** | More desktops | The `window` sensor on GNOME Wayland, KDE, sway, Hyprland; idle on wlroots. | #13 |

MVP 10 depends only on MVP 8 and can come before MVP 9 if meetings matter
more. MVP 11 can be done any time after MVP 7. The idle detection planned as
the first point of [MVP 3](mvp-3.md) moves into MVP 7.

## Model plan

Every ticket is split into steps, and each step goes to the cheapest model
that does it without mistakes. The goal is a balance: few tokens where the
work is mechanical, the strongest model where an error would spread
(interfaces, the time log, concurrency, privacy).

### Roles

| Model | Takes | Never takes |
|---|---|---|
| **Opus 5.5** | Design notes that fix interfaces, data model and invariants for later tickets; algorithms that must be exact (reconstruction, rounding); state machines and concurrency in the agent; anything that writes the time log; privacy and security reviews; the review at the end of each MVP | Boilerplate, catalogs, fixtures, UI polish |
| **Sonnet 5.5** | The default implementer: backends (X11, D-Bus, PipeWire, IPC), API endpoints, storage, web UI and panel, wiring, tests with edge cases, implementing an Opus design note | Changing an interface or invariant from a design note on its own (it escalates instead) |
| **Haiku 5.5** | Mechanical work with a precise brief: both i18n catalogs, settings schemas, docs and README lines, test fixtures and table tests from a given list of cases, example rules, `gofmt`/`go vet` findings, searches across the code | Design decisions, core logic, anything without a check that proves it right |

### Workflow per ticket

1. **Design note (Opus, only where the table says so).** Short, in the ticket:
   types and function signatures, invariants, the list of test cases. It is
   what makes the cheaper models safe: Sonnet implements against it, Haiku
   writes the listed tests.
2. **Implementation (Sonnet)** against the note, with `go test ./...` and
   `go vet ./...` green, `-race` for agent code.
3. **Mechanical parts (Haiku)** in parallel or afterwards, each with a check
   that fails if it is wrong (i18n catalog test, schema validation, tests).
4. **Review.** Sonnet reviews its own tickets only where Opus is not needed;
   Opus reviews every ticket that touches the core, the log, the agent's
   concurrency or privacy, and does one review pass over each MVP before it
   is called done (cheaper than per-commit reviews, catches what spans
   tickets).

**Escalation:** if Sonnet fails the same tests twice, needs to change a
design note, or the change touches `internal/core` invariants (event log,
corrections, `Settle`, `Apportion`), the step goes to Opus. Haiku output that
fails its check goes to Sonnet, not back to Haiku.

### Per ticket

| Ticket | Opus 5.5 | Sonnet 5.5 | Haiku 5.5 |
|---|---|---|---|
| #6 Sensor foundation | Design note: `Sensor` interface, observation model, API, where privacy filters run; review | Store table and retention job, `POST /api/observations`, offline queue, Setup → Devices | Fake sensor for tests, i18n, settings schema, docs |
| #2 Idle and return | Design note: absence state machine, mapping each answer to `off`/correction, interplay with session end; review | D-Bus and X11 backends, panel return dialog, notification fallback | i18n, settings, stack-test scaffolding from the listed cases |
| #1 Window on X11 | – (Sonnet reviews) | `xgb` backend, `PropertyNotify` handling, `detect()` | Tests with a fake window manager from given cases, docs |
| #7 Show observations | Short review of the `observe` scope | Day view band, MCP `list_observations`, scope in Setup | CSS, i18n, MCP tool description |
| #8 Rules and learned details | Design note: rule semantics, conflict order, confidence, core API; core classifier; review | Config parsing and validation, Setup rule list and rule test | Table tests from the note, example rules, i18n |
| #9 Live suggestions, auto-switch | Design note and implementation: dwell, hysteresis, manual-wins, auto start and revert (agent concurrency); review | Notifications with actions, tray hint, wiring | Settings, i18n |
| #4 Reconstruction | Algorithm (smoothing, blocks, scores, proposals), consistent with `Settle` and `Apportion`; review | Edge-case tests (midnight, overlaps, idle at edges), performance check | Docs, test fixtures |
| #10 Review in Day view | Check of proposal → correction mapping | Ghost entries, accept/reject, "accept all gaps" | CSS, i18n |
| #3 Smart prompts | – (Sonnet reviews; Opus in the MVP 9 pass) | Prompt scheduling, panel review mode | Settings, i18n, docs |
| #11 MCP blocks and proposals | Security review of the `propose` scope | Tools, scopes, Setup | Tool descriptions, tests from given cases |
| #5 Meetings (mic, cam) | – (design is in #8/#9; MVP 10 pass) | `pw-dump`/`pactl` parsing, `/proc` camera scan, meeting rules | JSON fixtures, example rules for Teams/Zoom/Webex/Meet, docs |
| #12 Notification names | Privacy review (text never stored or logged) | D-Bus monitor, rule evaluation, link to meetings | Example patterns (German, English), docs |
| #13 Wayland | – (Sonnet reviews) | GNOME extension and its install in `hat setup`, KWin script, sway and Hyprland IPC | Recorded-event fixtures, docs |

Rough share of the work: Opus about a fifth (the parts everything else
stands on), Sonnet about half, Haiku the rest.

## Not in this epic

- A browser extension for URLs, a shell hook for the working directory or git
  repository: strong signals, but each a separate install; possible later as
  further sensors.
- Calendar access (ICS link, Exchange, Google): MVP 10 works without any
  login on purpose; "Calendar entries as activity suggestions" stays in the
  outlook of MVP 3.
- Machine learning beyond rules and learned details. The MCP tools let an AI
  do the harder guessing, with the user confirming.
- Observations shared between users or sent anywhere but the own server.

## Decided

- An `auto` rule may start a hat when none is worn (2026-11-07). A per-rule
  choice (`start: ask`) can follow where it makes sense.
- Observations are kept 28 days by default (2026-11-07).
- The GNOME Shell extension is embedded in the binary and installed by
  `hat setup` (2026-11-07); extensions.gnome.org maybe later.
