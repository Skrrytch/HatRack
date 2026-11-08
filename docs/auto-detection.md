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

A sensor is a type of its own next to modules (`internal/sensors`): it has
backends with `detect()` like every module, but instead of `enter`/`leave`
hooks it emits observations while the agent runs. The device reports which sensors work, as it reports
its other capabilities today.

| Sensor | Observes | Backends (first ones in bold) |
|---|---|---|
| `window` | focused window: app (`WM_CLASS` or app id), title | **X11 via `xgb`** (`_NET_ACTIVE_WINDOW`, `_NET_WM_NAME`, `WM_CLASS`; event-driven through `PropertyNotify`); GNOME Wayland via a small Shell extension that `hat setup` installs; KWin script over D-Bus; sway and Hyprland IPC |
| `idle` | inactive since, screen locked | **logind `LockedHint`**, **`org.freedesktop.ScreenSaver`/Cinnamon/GNOME screensaver signals**, **X11 MIT-SCREEN-SAVER**, Mutter `IdleMonitor`, KDE `GetSessionIdleTime`; wlroots `ext-idle-notify` later |
| `media` | microphone capture stream active, camera open, and by which app | PipeWire (`pw-dump` JSON), PulseAudio (`pactl -f json`); camera: own processes holding `/dev/video*` (read from `/proc`, no root) |
| `notify` | desktop notifications matching a rule | D-Bus monitor on `org.freedesktop.Notifications.Notify`; text stays in memory, only a rule's result becomes an observation |
| `workdir` | repository and branch of the focused terminal or IDE, added to the window span (1a) | `/proc/<pid>/cwd` and `.git/HEAD`; IDE project lists for JetBrains, VS Code, NetBeans |

Explicitly not sensors: keyboard or mouse content, screenshots, browser
history, file contents, shell history, the session files of AI tools,
`/dev/input`.

The agent is a user service and may start at boot, before the desktop
session exists and without `DISPLAY`. It then takes the session's variables
(`DISPLAY`, `WAYLAND_DISPLAY`, `XAUTHORITY`, `XDG_*`) from the systemd user
manager as soon as the desktop has handed them over at login, so sensors and
the launch module reach the desktop without a restart.

### 1a. Working directory and branch (`workdir`)

Branch names carry the ticket key (`feature/ACME-101-login`), and so does the
work with Claude Code in that repository. Titles rarely show them: IntelliJ
names project and file, Claude Code replaces the terminal title with the
topic of its session. The `workdir` sensor finds the repository and branch
of the focused window locally, without network or login, from metadata only.

How (checked on Bert's machine, 2026-11-08):

- **Process:** the focused window's process id (X11 `_NET_WM_PID`; the GNOME
  extension, KWin, sway and Hyprland report it too), then its child
  processes in `/proc`. A known working tool among them (`claude`, `nvim`,
  `vim`, `git`) wins over the shell. Its working directory is
  `readlink /proc/<pid>/cwd` (own processes only, no root).
- **Branch:** from that directory upwards to `.git` (a directory, or a file
  `gitdir: …` for worktrees), then the one line of `HEAD`
  (`ref: refs/heads/<branch>`; a detached head gives no branch). Nothing else
  in the repository is read.
- **Claude Code:** the process is called `claude` and runs as a child of the
  shell in the terminal; its working directory is the project, so the branch
  comes from `HEAD` like any other, also after Claude Code switched branches
  itself. Its session files (`~/.claude/projects/*/*.jsonl`, or under
  `CLAUDE_CONFIG_DIR`) do carry `cwd` and `gitBranch`, but in the same lines
  as the conversation (`message`, `content`, `lastPrompt`, `aiTitle`), so
  HatRack never opens them.
- **Claude Code's title** (version 2.1.319): Claude Code sets the terminal
  title itself, first the session name from `/rename`, else a topic title a
  small model writes from the conversation, else "Claude Code".
  `CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1` switches that off, the setting
  `terminalTitleFromRename: false` keeps `/rename` out of the title. Bert's
  titles of 2026-11-08 show it: "Claude Code", "Claude WORK", a German topic
  title, and the shell's "user@host: ~/projects/…/my-repo" (folder, no
  branch). The options for a key in the title, judged:
  - an instruction in `CLAUDE.md` does not reach the title: the topic title
    comes from a separate model call that does not follow it, and a command
    Claude runs to set the title has no reliable terminal and is overwritten
    at the next update. Not reliable.
  - `/rename ACME-101 …` by hand works and stays (the session name outranks
    the topic), but must be done in every session.
  - a hook (`SessionStart`) writing the title with the branch needs the
    built-in title off, setup per user, and misses later checkouts;
    `UserPromptSubmit` would also see the prompt. The status line only
    shows inside Claude Code, never in the window title. Not worth it.
  - the title off plus the shell prompt snippet from a template keeps the
    folder and branch from the moment Claude Code was started.
  So the sensor reads the branch from `/proc`, and the title stays a
  fallback for users without the sensor (`/rename`, or the title off plus
  the prompt snippet), described in the toolbox (3c).
- **IDEs:** IntelliJ's title names project, file and module
  ("my-repo – <file> [module]" in real data), no branch. The IDE process
  does not run in the project, so the title's project name is mapped to its path through the IDE's own list of recent
  projects: JetBrains `~/.config/JetBrains/<product><version>/options/recentProjects.xml`
  (entries keyed by project path, with `customProjectName`), VS Code
  `~/.config/Code/User/workspaceStorage/*/workspace.json`, NetBeans similar
  (evaluated in the ticket). Only paths and names are taken from those files.
- **IDE plugins, judged:** the ActivityWatch watcher for JetBrains
  (`aw-watcher-jetbrains`, source read 2026-11-08) does send `branch`, taken
  from the IDE's Git support, but only of the project's first repository,
  together with `project`, `projectPath`, `file`, `fileFullPath`, `commit`,
  `sourceUrl` (the remote, which can hold a token) and the language, to a
  running ActivityWatch server (`localhost:5600`); last release 2023, one
  plugin per IDE family. WakaTime-style plugins send heartbeats through their
  own command line tool to a cloud service with an API key (or a self-hosted
  server). Either way: something extra to install and run, more data than a
  branch, and a server HatRack would have to imitate or query. Rejected;
  `/proc` plus `HEAD` covers terminals, Claude Code and every IDE with nothing
  installed. Reading the buckets of an ActivityWatch the user already runs
  can come later.
- **Terminals with tabs:** Guake, GNOME Terminal and tmux run one process for
  all tabs, so `/proc` alone does not tell the visible tab. Rule: use a result
  only if all shells of the window agree, if the title names a folder (the
  shell's own title, as in Bert's Guake) that matches one shell's working
  directory, or if the shell whose `/dev/pts` was typed into last can be told
  apart (its access time; to verify on the desktop); otherwise record nothing
  rather than guess. Bert's terminal is Guake, so this matters from day one.
- **When:** on every focus change and every 30 s while a terminal or IDE has
  the focus (a checkout changes `HEAD`); a changed repository or branch starts
  a new window span, as a changed title does.

What is stored, as attributes of the window span: `repo` (the name of the
work tree's folder, never a path) and `branch`, the latter
only if it contains a ticket key of a known project (decided, see
[decisions.md](decisions.md)); other branch names are dropped. The privacy settings apply
first: an `ignore` or `redact`
app gets neither, a hat with `observe: false` neither, and folders on the new
list `observe.ignore_paths` (e.g. `~/privat`) are never looked up. The
sensor is off until enabled per device, like `window`.

The classifier treats `branch` like a title from a working tool: rules may
match it, a ticket key in it outranks one from the browser (3a), and `repo`
counts as a title part for learning.

### 2. Observations

An observation is a span with constant attributes:

```
observations(id, device_id, sensor, start, end, attrs_json, rev)
  window : {app, title}                       (title may be redacted)
  idle   : {kind: inactive | locked}
  media  : {mic: bool, cam: bool, app}
  notify : {rule, value}                      (e.g. value "Sprint Planning")
```

Titles are normalized before they are compared and stored: status symbols
and marks at both ends (spinners, dots, emoji, `*`), a leading unread counter
like `(3)` and repeated spaces go, so a title whose status icon changes stays
one span. Changes inside a title (timers, percentages) stay.

- The agent coalesces: a new span starts only when the attributes change. A
  workday is a few hundred to about two thousand spans.
- **Before anything leaves the device** the privacy settings apply:
  `ignore` apps are dropped (only "something private" with its length
  remains), `redact` apps keep only the app name, and while a hat with
  `observe: false` is worn only `idle` is recorded.
- Spans go to the server in batches (`POST /api/observations`) through the
  existing offline queue. The server keeps them for `observe.retention_days`
  days (default 28; titles are sensitive, and four weeks cover any review) and
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
    - match: {app: "jetbrains-|^code$|netbeans"}
      activity: [Implementierung, Entwicklung]   # the first one the hat has
```

- `match` fields are regular expressions on the span's attributes (`app`,
  `title`, `notification`) or flags (`media: mic | cam`). All given fields
  must match.
- The hat is the one the rule is defined in; `activity` and `detail` are
  optional, `detail` may use capture groups.
- **Global rules** (`defaults.rules`) name no hat. Besides `ignore` they may
  give an activity and a detail, which join whatever names the hat (a hat
  rule, a learned detail or title, live also the worn hat). Activity names
  differ per hat, so `activity` may be a list: the first one the hat has
  wins, none means no activity. This is how "an IDE means development"
  works for every hat with one rule.
- `app` is matched without regard to case: the X11 class is `Code`,
  `Vivaldi-stable`, `jetbrains-idea`, the Wayland app id may differ.
- The classifier normalizes titles itself (`core.NormalizeTitle`), so spans
  stored before normalization classify the same.
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

A ticket key counts only if its project is known: from `core.TicketLinks`,
from a booked detail, or named in a rule. `UTF-8` or `ISO-8859` in a title
are no tickets.

**Learned title parts** without any configuration: titles are split at
their separators (` - `, ` – `, ` — `, ` · `, ` | `) into parts such as an
IDE project, a repository, a Confluence space or a file name. A part seen
mostly while one hat was booked (default: at least 30 minutes within the
retention period, 80 % of its time on that hat; title parts are not kept
longer than the observations they come from) suggests that hat with lower
confidence (0.5). Parts seen under many hats (`Vivaldi`, `Visual Studio
Code`, `Jira`) carry no hat. This covers what rules cannot know in advance,
for example a terminal whose title is the topic of a session and names no
project (seen in the first data of MVP 7).

A span no rule or detail matches has no candidate ("unknown").

### 3a. Strategies for work tools

The primary users are developers and DevOps engineers, then office work.
Their tools show in the window title what they work on, often with a ticket
key. The classifier uses four layers of evidence, from weak to strong; one
span may get its hat, activity and detail from different layers:

| Layer | Tells | Example | Source |
|---|---|---|---|
| Tool | the activity | IDE → Implementierung, Confluence → Dokumentation, a pull request → Code-Review | global rule, often from a template |
| Context | the hat | IDE project, repository, Confluence space, Jira project, file name | hat rule or learned title part |
| Ticket key | the detail, and the hat through the project | `ACME-1234` in a Jira title, a branch `feature/ACME-1234-login`, a file name | global rule, learned detail, `TicketLinks` |
| History | all three | what was booked while the same detail or title part was on screen | learned |

What the tools show, as far as it is typical. This table is a starting
point, not the catalogue: #15 first evaluates which user groups and tools
matter, and every template is checked against recorded titles before it
ships.

| Group | Tools | Title shows | Notes |
|---|---|---|---|
| IDE | IntelliJ IDEA and other JetBrains IDEs, NetBeans, VS Code, Eclipse | project, file; VS Code also the folder | VS Code can show the branch: `window.title` with `${activeRepositoryBranchName}`. Visual Studio runs on Windows only and is out of scope. |
| Git | branches, pull/merge requests | branch only where the tool shows it; PR title and repository in the browser | branch names usually carry the ticket key. The `workdir` sensor (1a) reads the branch of the focused terminal or IDE, Claude Code included; without it a terminal shows repository and branch only if the shell prompt sets the title (a template carries the snippet). |
| Tickets | Jira, YouTrack, Redmine, GitHub and GitLab issues, ServiceNow, Znuny/OTRS | key and summary, project or space | Jira-shaped keys `ABC-123`; others (`INC0012345`, `Ticket#…`) through template rules |
| Docs | Confluence, wikis | page and space | the space usually names the project |
| Office | LibreOffice, ONLYOFFICE, Microsoft 365 in the browser | file name | file names often carry customer, project or key |
| PDF | Xreader, Evince, Okular, the browser | file name | like office documents |

Consequences for the stages:

- **Live (#9):** a new ticket key under the same hat suggests a new detail,
  not a switch, and fills an empty detail by itself only with `mode: auto`.
  A short visit to another tool (a look at Jira while coding) changes no
  activity: the dwell applies to activities as to hats. A key from a working
  tool (IDE, branch) outranks one from a reading tool (browser).
- **Afterwards (#4):** within a run of one hat, the key last seen carries
  over to spans without a key (the IDE between two Jira views) until another
  key appears, the hat changes or an absence intervenes.
- **Asking and review (#3, #10):** the prefilled answer and every proposal
  name the key and where it came from ("ACME-1234, from branch
  feature/ACME-1234-login").
- **MCP (#11):** block summaries give the tool groups per block with `read`;
  keys taken from titles only with `observe`.
- **Notifications (#12):** only calendar reminders; ticket notifications are
  not read (see the conflict noted in #12).

### 3b. Rule templates (#15)

Nobody should write regular expressions for Jira or IntelliJ by hand. A
library of rule templates lives in the repository as one JSON file, is
embedded in the binary (works offline, nothing is ever fetched) and is
readable by anyone on GitHub, so others can add templates by pull request.

- A template has an id, a version, a category (`ide`, `git`, `tickets`,
  `docs`, `office`, `pdf`, `terminal`, `meeting`, `ignore`), names and a
  description in German and English, parameters (target hat, activity from
  the hat's list, project keys prefilled from `TicketLinks`, a space or
  repository name), the rules with placeholders, a link to the toolbox page
  for setup (3c: VS Code `window.title`, the shell prompt snippet), app
  names for X11 and Wayland, and examples: app and title with the expected
  result.
- Using one: Setup → Erkennung → Regeln → "Add from template": pick, fill
  the parameters, see in the rule test what it would match on the last day,
  save. The rules are copied into the configuration with a note of their
  origin (`template: jira@3`), so export and import keep working and a newer
  library never changes saved rules; Setup only says that a newer version
  exists.
- New templates come with a new HatRack version; the server fetches nothing
  (product.md: nothing is fetched from outside). Own templates (a company's
  Jira) are imported as a file; a file that fails validation changes nothing
  and is reported with the reason.
- A test checks the file: schema, every expression compiles, every example
  gives its expected result, ids are unique.

**Evaluation first.** Before the library is filled, #15 works out what is
useful, beyond the examples above, and records it in
`docs/rule-templates.md`:

- **User groups:** developers and DevOps (primary), QA, product owners and
  Scrum masters, project managers, business analysts, IT support and
  operations (ITSM), consultants and freelancers who bill per customer,
  office and administration.
- **Per group:** the tools in daily use on Linux and in the browser (SaaS),
  what their titles show, and which identifiers the work is booked by
  (ticket keys, customer names, project or order numbers, cost centres).
- **Sources:** public surveys (e.g. developer surveys for IDE shares), the
  documentation of the tools, recorded titles from real machines, requests
  from users (an issue template "template wanted"). No telemetry: HatRack
  sends nothing to collect usage.
- **Criteria per tool:** reach (how many users), signal (does the title say
  hat or detail, or only the tool), stability (title formats change between
  versions and with the UI language, German and English at least), privacy
  (mail subjects and chats are sensitive: rather `redact` than a rule),
  effort.
- **Result:** a ranked catalogue: first wave, later, not suitable (with the
  reason). The first wave ships with #15, the rest follows by pull request.

In the user's own Setup the same idea works locally: the rule test lists the
apps of the last days that no rule matches and offers the templates that fit
them. Nothing of that leaves the server.

### 3c. Toolbox: setting up work tools (new ticket)

Rules read what a tool shows; some tools show more once set up: VS Code puts
the branch into its title with one setting, a shell prompt can set the
terminal title to folder and branch, Claude Code takes a session name from
`/rename`. Such setup belongs in a **toolbox** of how-tos, one page per tool,
not into rules and not into sensors.

Each page:

| Part | Content |
|---|---|
| Goal | what HatRack should see afterwards ("the branch in the title of VS Code") |
| Setup | the setting, snippet, plugin or instruction to copy, with the tool versions it was checked with |
| Check | how to see that it works: the tab Zuordnung shows the tool's rows with the key and its source ("ticket from title") |
| Privacy | what the tool then shows to anyone looking at the screen, and to HatRack; hooks that see prompts are not offered |
| Not needed if | what the `workdir` sensor (1a) or a template (#15) already covers |

First pages: VS Code (`window.title` with `${activeRepositoryBranchName}`),
the shell prompt title for bash and zsh, Claude Code (rely on the sensor;
`/rename` with the key works by hand; an instruction in `CLAUDE.md` to name
the key is not proven: Bert's first title after giving it, 2026-11-08 12:34,
named the work but no key, which fits a topic title written by a separate
model call; it stays "to try, check in Zuordnung"), IntelliJ (a branch in the
frame title by setting or plugin, to evaluate; with the sensor not needed),
Jira and Confluence (title formats, language), Thunderbird (subjects:
`redact` or rules, privacy first).

Where: Markdown in `docs/toolbox/` in the repository, readable on GitHub and
open to pull requests like the templates. A template (#15) links to its page
(`setup: toolbox/vscode.md#branch`) instead of carrying setup hints itself.
In Setup the rule test names, for a tool whose rows lack a key, the page that
would help (a one-line hint in both languages and a link); nothing is fetched
for that.

Toolbox and sensor, honestly: the sensor works without anything the user
does, for every terminal, Claude Code and IDE, as long as the process can be
told (one tab or a clear match) and the branch carries the key. The toolbox
covers what no sensor sees (titles of Jira, Confluence, mail, office files),
helps where the sensor cannot decide (a prompt title names the folder of the
visible tab) or is missing (desktops without a process id, users who leave
it off), but needs setup per tool and machine, drifts with tool versions,
and some tips are unreliable (the Claude Code instruction so far). So:
branches come from the sensor, the toolbox fills the gaps.

Ticket: its own, next to #15 and not inside it. #15 is code and data
(library format, loader, validation, "Add from template"); the toolbox is
mostly prose that can ship before any code. Both use the tool list from the
evaluation of #15 (`docs/rule-templates.md`) and the same tool ids, so a
template and its page find each other.

### 4. Live behaviour (agent)

The agent classifies spans as they happen, against the cached configuration,
so it works offline.

- **Dwell and hysteresis:** a candidate counts only after it held for a while
  (idle excluded); short visits never trigger anything: 10 of the last 15
  active minutes, and a new detail only after the old one was not seen for
  15 minutes (6a).
- **Manual wins:** after a manual switch, nothing is suggested or switched
  for 15 minutes unless a rule with `mode: auto` and a hardware signal
  (microphone) fires.
- **`suggest`:** a hint on the tray icon; a notification with an action
  ("Wechseln zu VENOM · Entwicklung · ACME-142") only when the user turns on
  `live.notify` (default off). Ignoring it is fine.
- **`auto`:** the switch happens, backdated to its start; the event carries
  `source: rule:<id>`, the tray hint offers "Rückgängig". When the condition ends (microphone off)
  the previous hat and activity come back, also with `source`.
- **`auto` also starts a hat** when none is worn or the clock is paused: the
  most automatic behaviour is the default. Where a choice makes sense, a
  rule can later get `start: ask` (offer instead of start); not in the
  first version.

### 5. Idle and return

Idle is the base of everything else: idle time is subtracted before any
score is computed. Built in #2:

- The `idle` sensor reports inactivity from 60 s on, backdated to its start,
  and a lock at once (backends `x11` for Cinnamon, `mutter`, `kde`, `logind`).
  The agent counts it as an absence after `observe.idle.after` (5 min); a
  suspend counts too (a gap in the agent's own ticks).
- After `stop_after` (15 min) the agent stops the clock backdated to the start
  (`off`), so a computer locked overnight is no working night, but only if no
  other computer is in use: it asks `GET /api/presence` (online, idle sensor
  known, away). An online computer whose absence is unknown blocks the stop;
  an unreachable server does not.
- **Nothing blocks:** on return the hat runs again by itself and the absence
  stands as a pause; between `ask_after` and `stop_after` the clock ran on.
  Then the tray opens the panel with the question ("Pause" / "War Arbeit" /
  pick a hat or activity); without a tray a notification with a button, and
  `hat away keep|pause|work|<hat>` on the command line.
- Answers book plain backdated `off`/`switch` events, never corrections
  (devices may not send them); the live state ignores older events, so the
  current hat is never disturbed. An answer is refused once another event
  came after the return.

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

Steps 3 and 4 are replaced by condensing (6a): hour windows instead of
15-minute blocks, one entry per task and window instead of one per run, and
tickets worked on in turn as one parallel entry.

### 6a. Parallel work and the end of a task

Classifying spans is half the job. Two tickets worked on in turn plus a mail
about a training make 20 window spans in an hour; booked one by one, nobody
can read that log. This section says when a task ends and how parallel work
becomes a short, true report. Decided on 2026-11-08 (see
[decisions.md](decisions.md)).

Terms:

- **Task:** hat, activity and detail of a run (`VENOM · Entwicklung · ACME-101`).
- **Episode:** active time without a hard end (table below).
- **Window:** the part of an episode within one clock hour; the unit for
  condensing, proposing and asking. It matches the hour rows of *Observed*.
- **Anchor:** a boundary the user set: a switch, activity, detail or off made
  by hand, or a correction they made. Boundaries from rules (`source:
  rule:…`) and accepted suggestions are no anchors.
- **Memory:** 15 minutes of active time (`reconstruct.memory`).

#### When does a task end?

The start shows (the first span with the key); the end mostly does not,
because leaving a window says nothing, people come back. So: **a task stays
open until something ends it.**

| Signal | Effect | Available |
|---|---|---|
| absence from `idle.after` (5 min), lock, suspend, session end | hard: ends the episode | now (#2) |
| a boundary the user sets (anchor) | hard | now |
| microphone or camera, meeting reminder | hard, starts the meeting | MVP 10 (#5, #12) |
| another hat holds 10 of the last 15 minutes | ends the hat | live (#9); afterwards implicit |
| the key not seen for the memory | soft: the task ended at its last sighting | derived |
| a new key from a working tool (IDE, branch) | starts a task, ends none | titles now; branch from the `workdir` sensor (1a) |
| a branch switch in the repository | starts a task (new key), ends none | `workdir` sensor (1a) |
| end of working hours | ends nothing, triggers the end-of-day question | config |
| commit, push | – | not observed: would need git hooks in every repository; the branch carries the key anyway |
| ticket status, calendar | – | not planned: HatRack fetches nothing and logs in nowhere |

Consequences:

- Time without any candidate (a terminal without project, a file dialog)
  belongs to the task last seen, up to the memory; longer stays unknown. A
  span that names a tool of its own (mail client → Kommunikation) ends the
  carry: it is unknown, not the ticket.
- Idle shorter than `idle.after` belongs to the surrounding task, as the clock
  already treats it.
- Nobody has to pick the minute a task ended: afterwards only the sums per
  window count, live only the main task.

#### How is parallel work captured?

Measured on 300 synthetic days with a prototype outside the repository
(about 265 spans and 8.3 active hours a day: phases on one ticket, phases
with two tickets alternating every 6 minutes on average, mails of 3 to 10
minutes, a meeting; evidence noise: terminals without key 15 %, short looks
at chat or ignored apps 10 %, glances at the other ticket in Jira 6 %, mails
without a rule 30 %). *Sum error*: share of time on the wrong task in the
totals, in brackets what the method adds with flawless evidence. *Moved*:
share of minutes booked where something else was done at that clock time
(a parallel entry counts as right for each of its tickets).

| Method | Entries per hour | Sum error | Moved | Verdict |
|---|---|---|---|---|
| runs as observed (noise and memory only) | 7.0 | 6.0 % (0.0) | 17 % | too many entries |
| in order, pieces under 3 min absorbed | 4.0 | 5.5 % (1.7) | 17 % | true order, still 6 per hour when two tickets alternate |
| in order, pieces under 10 min absorbed | 1.6 | 9.6 % (7.8) | 24 % | the second ticket disappears |
| condensed per hour, pieces under 5 min absorbed | 2.3 | 5.8 % (1.7) | 31 % | totals per ticket, order inside the hour condensed |
| **condensed per hour, alternating tickets of one activity as one parallel entry** | **2.1** | not split per ticket for that time | **16 %** | **decided** |
| condensed per episode | 1.3 | 5.7 % (0.3) | 51 % | moves time by hours |
| one entry with several details and stored shares | 1–2 | as condensed | – | changes interval, totals, CSV, MCP, learning |
| overlapping side entries | – | – | – | breaks "one hat at a time, no overlaps" (2026-11-04) |
| split only in the report, from observations | – | – | – | observations expire after 28 days; reports come from the log |

The evidence costs about 6 % whatever the method, so better evidence (rules,
#15, the branch from 1a) matters more than any threshold. Within one
activity, the split between two tickets worked on in turn is exactly where
the evidence is weakest (a glance at the other ticket, the IDE open while
thinking); only the person who did the work knows it. So such time is not
split but named: **one entry with both tickets as its detail**. Different
activities or hats are still split by their sums, because they are booked
and billed apart.

**Decided:** an entry keeps one hat, one activity and one detail (no model
change). Afterwards each window becomes one entry per task with its summed
time; tickets of one hat and activity worked on in turn become one parallel
entry. Live, HatRack follows only the main task.

#### Condensing (core, afterwards)

1. **Clean** the classified spans of an episode: idle out; noise (under 60 s)
   and `ignore` to the task they interrupted; memory and detail carry as
   above. Result: runs with one task each, or unknown.
2. **Cut** at anchors and full hours. The user's boundaries stay where they
   are.
3. **Sum** active seconds per task and window. A task under
   `reconstruct.min_entry` (5 min) goes to the biggest task of the window with
   the same hat and detail, else the same hat, else the biggest one, also
   across hats (named in the reason); never between a working hat and one
   with `work_time: false`. Unknown time of 5 minutes or more stays a line of
   its own and is asked about.
4. **Group parallel work:** tasks of the same hat and activity, each with at
   least `min_entry` in the window and at least two pieces of 2 minutes or
   more (they alternate), become one task whose detail lists the keys in
   sorted order: `ACME-101, ACME-117`. Sorted, so the same pair always adds up
   to one line in the totals. A task of another activity or hat in between
   stays apart.
5. **Lay out** the tasks one after another from the window's start, in the
   order they first appeared; the task running at the window's end goes last,
   so the next hour continues it. A task that opens and closes the hour is
   split by its time before and after the half hour (frame rule). Minutes are
   apportioned to whole minutes (`core.Apportion`), so entries start on full
   minutes and the window adds up. Equal neighbours across hours merge.
6. **Compare** with the log by sums, not positions: a window agrees when every
   task's booked and observed time differ by less than `min_entry`. A
   parallel task agrees with booked entries of its hat and activity whose
   details name only its keys (`ACME-101, ACME-117` or `ACME-101` and `ACME-117`
   apart, as the user split them). So an accepted proposal never comes back
   (invariant), and an hour booked by hand with the right totals is left
   alone.
7. **Propose** gaps and disagreements per window. Each entry carries its
   confidence (time-weighted) and its reason: sources, number of pieces, what
   it absorbed ("includes 40 s KeePassXC"); a parallel entry also its
   observed shares and where each key came from ("ACME-101 about 31 min,
   branch feature/ACME-101-login; ACME-117 about 20 min, Jira").

Accepting writes one `correction` with `source: suggestion` per window.

What a parallel entry means downstream:

- **Totals and CSV:** one detail line `ACME-101, ACME-117`; the time is not
  split per ticket. Ticket links work for both keys (`core.SplitDetail`).
- **Learning:** both keys learn the hat and activity of the entry
  (`learnables` already finds every key in a detail).
- **Per-ticket booking** (the export of MVP 3): needs a split; the review
  offers it, and the export asks or books the pair as one item.
- **Marker:** none stored; an entry is parallel when its detail names two or
  more ticket keys, so entries typed by hand count too.

In the simulation 39 % of the time ended in parallel entries (the synthetic
days alternate about 45 % of the time); 11 % of that was really worked in
sequence, misread because of a glance. Requiring longer pieces lowers the
false part only a little (8 % at 3 minutes) but hides real alternation, so 2
minutes it is. The review shows the shares and splits with one click.

#### Live (#9)

Live cannot know whether you come back, so it books the main task and leaves
the split to the review:

- **Another hat** is suggested once it holds 10 of the last 15 active minutes
  (instead of 3 minutes in a row), backdated to its start. An 8-minute mail
  triggers nothing; a forgotten switch is noticed after 10 minutes and still
  booked from its start. `auto` rules with a hardware signal stay immediate.
- **A new detail** under the same hat is suggested only when the old key has
  not been seen for the memory and the new one holds 10 of 15 minutes. Two
  tickets in turn stay one entry live.
- In the simulation (every suggestion accepted) the 3-minute dwell gives 3.8
  entries per hour, this rule 1.2; the review then splits by the sums.

#### Asking and reading

- **Review (#10):** the proposal appears under its hour in *Observed*: a ghost
  row with the condensed entries, Accept or Reject per hour, "Accept all" per
  day. A parallel entry shows a "parallel" mark and its observed shares, with
  "Split" (two entries by the shares) next to Accept. Mockup before building.
- **Calls for help (#3):** the unit is the window, one line per proposed entry;
  asked are windows with unknown time or a disagreement. Pieces under 5
  minutes and parallel entries never cause a question by themselves.
- **Day report (new ticket):** a view of the log, not the observations.
  Entries without a pause between them form a phase, and a phase reads as one
  line with its totals per hat, activity and detail: `10:00–11:00 · VENOM
  Entwicklung: ACME-101 + ACME-117 (parallel) 51 min · Office
  Schulungsvorbereitung 9 min`. It reads well even when the log has many
  small entries (also booked by hand) and is the base of the daily summary of
  [MVP 3](mvp-3.md).

#### Worked example: one hour, two tickets, one mail

```
10:00 IDE feature/ACME-101-login · Jira ACME-101 · terminal ~/venom-api · Slack 40 s · IDE
10:14 Jira ACME-117 (bug report) · IDE feature/ACME-117-export · KeePassXC 40 s · terminal
      · Jira ACME-101 90 s (a glance) · IDE ACME-117
10:29 Thunderbird "AW: Schulung Git-Grundlagen – Agenda" · Slack 50 s
10:37 IDE ACME-101 · terminal
10:47 IDE ACME-117 (build failed)
10:53 Spotify 30 s · IDE ACME-101 · Jira ACME-101
```

20 spans. Cleaning leaves 8 runs: ACME-101 14:00, ACME-117 11:00, ACME-101
1:30, ACME-117 2:30, Schulungsvorbereitung 8:50, ACME-101 9:10, ACME-117 6:30,
ACME-101 6:30. Sums: ACME-101 31:10, ACME-117 20:00, Schulungsvorbereitung 8:50;
nothing under 5 minutes. ACME-101 and ACME-117 alternate with pieces of 2
minutes and more, same hat and activity: one parallel task of 51:10. It
opens and closes the hour and is split at half past (29:00 before, 22:10
after):

| Time | Entry | Min |
|---|---|---|
| 10:00–10:29 | VENOM · Entwicklung · ACME-101, ACME-117 (parallel) | 29 |
| 10:29–10:38 | Office · Schulungsvorbereitung | 9 |
| 10:38–11:00 | VENOM · Entwicklung · ACME-101, ACME-117 (parallel) | 22 |

The mail lands at its real time (10:29–10:37:50). The reason says
"ACME-101 about 31 min (branch feature/ACME-101-login), ACME-117 about 20 min";
done in fact were 29:40 and 21:30, the difference is the 90-second glance.
"Split" gives 10:00–10:15 ACME-101, 10:15–10:35 ACME-117, 10:35–10:44 Office,
10:44–11:00 ACME-101. In order with 3-minute absorption the hour gives 6
entries; the 3-minute live dwell would have asked 5 times, the rule above not
once (the hour stays ACME-101 until the review).

#### Effect on the tickets

| Ticket | Change | Models |
|---|---|---|
| #4 | condensing replaces 15-minute blocks and run-based proposals: memory, anchors, hour windows, sums, absorption, parallel grouping, layout with frame rule, comparison by sums; the worked example and the invariant as tests | Opus: design note and core; Sonnet: edge cases (midnight, DST, two computers active at once, anchors inside an hour); Haiku: fixtures |
| new: `workdir` sensor (1a) | repository and branch of the focused terminal or IDE, so branch keys (also from Claude Code) reach the classifier | Opus: short design note and privacy review; Sonnet: `/proc`, `HEAD`, IDE project lists, classifier fields; Haiku: fixtures, docs |
| #10 | proposal per hour under *Observed*, accept per hour or day, "Split" for parallel entries, reason names absorbed pieces | Sonnet after a mockup; Opus checks one correction per window |
| #9 | moves to MVP 9: 10 of 15 minutes, detail only after the memory, backdated; memory from the core | Opus (state machine), Sonnet notifications |
| #3 | the window is the unit, one line per entry | Sonnet |
| #11 | `summarize_blocks` returns windows with sums, parallel marks and proposed entries | Sonnet; Haiku descriptions |
| new: day report | phases from the log, parallel entries as `A + B (parallel)` | Sonnet after a mockup; Haiku i18n |
| new: toolbox (3c) | how-tos per tool in `docs/toolbox/`, linked from templates and the rule test | Sonnet writes and checks the pages; Haiku skeletons and i18n; Opus privacy review |

Order: #4 (core), with the `workdir` sensor alongside (independent, better
evidence) → #10 → #9 → #3 → #11 → day report (no dependency, can come
earlier). The toolbox (3c) follows the evaluation of #15 and ships its first
pages before the templates; it needs no code until the rule test links it.

Risks: condensed times look wrong to someone who remembers the mail at half
past ten (*Observed* shows the order under the hour; parallel entries keep
most positions); absorbing under 5 minutes of another hat moves minutes
between customers (named in the reason, never across working and
non-working hats); about a tenth of the parallel time was really sequential
(the shares and "Split" make it visible); evidence errors outweigh the
method; two computers active at once need a rule in the design note of #4.

### 7. Review and asking

- **Day view:** below the logged band an "observed" band shows the runs in
  their hat colours (hatched when unsure, grey when unknown). Proposals appear
  as ghost entries with Accept / Reject; "Accept all gaps" for a whole day.
  Accepting writes one `correction` with `source: suggestion`.
- **Calls for help:** instead of a fixed hourly question, HatRack signals
  quietly: a mark on the tray icon with the number of open hours and a "Needs
  you" list in the Day view. A notification is a choice (`prompt.notify`,
  default off): when at least `prompt.min_open` (default 20 min) is unbooked
  or disagrees, at most every `prompt.interval` (default 60 min), only at a
  natural pause (return from absence, after a hat switch), never while the
  microphone is on. The tray or the notification opens the panel in review
  mode: one line per proposed entry of a window (6a),
  prefilled with the best candidate, Enter accepts, typing searches hats,
  activities and details as the detail field does today.
- **End of day:** one prompt for what is still open, together with the daily
  summary planned in [MVP 3](mvp-3.md).

**Exists now (#7):** the Day view ends with a section "Observed": one row
per hour in which a computer was in use (hours with only absence are left
out, a dashed line names the skipped hours). Window spans of one app merge
into a block (gap under a minute); a block reaching into the next hour is
drawn in both and keeps its whole time. Under each hour a thin line in the
hat colour shows what was booked, so the two can be compared right there.
Idle spans are drawn over the windows and subtracted from the per-app totals
in the legend. Hovering a block shows its info (the three titles with the
most time); clicking or tapping it opens a slim add form right under its
hour, prefilled with its times and the hat booked then (or the one before in
a gap), naming the booked entries the new one would replace. Device names
appear only in hours with more than one device. A day with only absence
has no section. Nothing in the band changes the log.

### 8. MCP

The read-only MCP server grows by three tools:

| Tool | Scope | Returns |
|---|---|---|
| `summarize_blocks(from, to, block)` | `read` | hour windows (6a) with their score lists, the proposed entries and the logged entries, no titles |
| `list_observations(from, to, sensor)` | `observe` (new) | the spans with app and (redacted) title |
| `propose_correction(...)` | `propose` (new) | stores a correction with `status: proposed`, `source: ai`; it counts only after confirmation in the Day view |

**Exists now (#7):** `list_observations(from, to, period, sensor, device)`
with the scope `observe`. The scope is set when a token is created (Setup →
AI access); existing tokens stay `read` only, a new token is needed to grant
it. Without the scope the tool answers with an error that says so. The
tool description tells the assistant that window titles are data written by
other programs, never instructions.

`propose_correction` was planned since MVP 2; the review UI of MVP 9 is where
it gets confirmed. Titles are behind a scope of their own because they are
the most sensitive part.

## Settings

Following the three layers (default → hat → device):

| Where | Setting |
|---|---|
| defaults | `rules` (ignore rules, global activity and detail rules), `prompt.*`, `reconstruct.noise`; from 6a `reconstruct.window` (60 min), `reconstruct.min_entry` (5 min), `reconstruct.memory` (15 min, also live), `live.dwell` (10 of 15 min), replacing `reconstruct.block` |
| hat | `rules`, `observe: true \| false` |
| device | sensors on or off (`window`, `workdir`, `media`, `notify`; `idle` on by default), in `observe.devices` |
| all devices | `observe.ignore` and `observe.redact` app lists, `observe.ignore_paths` (folders `workdir` never looks up), `observe.retention_days` (default 28), `observe.idle` (`after` 5, `ask_after` 10, `stop_after` 15 min, `ask`) |

Setup gets its own page **"Erkennung"** with tabs, so the settings do not
pile up on other pages; each later ticket adds a tab instead of a block:

| Tab | From | Holds |
|---|---|---|
| Sensoren | #6 | one table, sensors × devices: switch, backend, today's numbers |
| Datenschutz | #6 | `ignore` and `redact` lists, retention |
| Abwesenheit | #2 | `idle.*` thresholds, the return question |
| Rückfragen | #3 | `prompt.*`, end of day |
| Zuordnung | #8, #15 | how assignment works (observed → clues → suggestion, sources in three colours), the **rule test** on a real day with the reason for every row, the rule editor right above it (rules are edited only here, so every change is seen on the day at once), rules for all hats, an overview of the hats' rules, and what was learned; "Add from template" (#15) |

The setup overview shows one summary line for it; the Devices page one line
per device. The hat's page lists its rules as sentences with what they matched in the
last 7 days and what else points to the hat (tickets, learned clues); the
links open the editor in the tab Zuordnung.

## Stages

Each stage is usable on its own. The order follows dependencies: nothing
works without observations, nothing afterwards without rules.

| Stage | Name | What you get | Issues |
|---|---|---|---|
| **MVP 7** | Observe | Idle and lock handled, return question; window titles recorded on X11; an "observed" band in the Day view and the raw spans over MCP. Booking stays manual, but you can see what you did. | #6, #2, #1, #7 |
| **MVP 8** | Assign | Rules per hat and global, learned details and title parts, rule test in Setup, rule templates for common work tools, a toolbox of how-tos for setting tools up; repository and branch of the focused terminal or IDE. | #8, #15, new toolbox, new `workdir` sensor |
| **MVP 9** | The reliable day | Condensing per hour with parallel entries and proposals, review in the Day view, live switching of the main task, calls for help, MCP window summary and AI proposals, day report by phases. | #4, #10, #9, #3, #11, new day report |
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
| #15 Rule templates | Design note: file format, parameters and placeholders, how a template becomes rules; review | Embedding, import of own templates with validation, "Add from template" in Setup | Template content and examples for the listed tools, i18n, docs |
| #9 Live suggestions, auto-switch | Design note and implementation: dwell, hysteresis, manual-wins, auto start and revert (agent concurrency); review | Notifications with actions, tray hint, wiring | Settings, i18n |
| #4 Reconstruction | Algorithm (smoothing, blocks, scores, proposals), consistent with `Settle` and `Apportion`; review | Edge-case tests (midnight, overlaps, idle at edges), performance check | Docs, test fixtures |
| #10 Review in Day view | Check of proposal → correction mapping | Ghost entries, accept/reject, "accept all gaps" | CSS, i18n |
| #3 Calls for help | – (Sonnet reviews; Opus in the MVP 9 pass) | Prompt scheduling, panel review mode | Settings, i18n, docs |
| #11 MCP blocks and proposals | Security review of the `propose` scope | Tools, scopes, Setup | Tool descriptions, tests from given cases |
| new `workdir` sensor | Short design note (what is read and stored, tab rule); privacy review | `/proc` walk, `HEAD` parsing (worktree, detached), IDE project lists, X11 pid, classifier fields `branch`, `repo` | Fake `/proc` and repository fixtures, i18n, docs |
| new toolbox | Privacy review of the snippets (no hook that sees prompts) | Pages checked against recorded titles, link from the rule test, `setup` link in templates | Page skeletons from the table in 3c, i18n of the hints, docs |
| new day report | – (Sonnet reviews) | Phases from the log, rounding with `Apportion`, view after a mockup | i18n, docs |
| #5 Meetings (mic, cam) | – (design is in #8/#9; MVP 10 pass) | `pw-dump`/`pactl` parsing, `/proc` camera scan, meeting rules | JSON fixtures, example rules for Teams/Zoom/Webex/Meet, docs |
| #12 Notification names | Privacy review (text never stored or logged) | D-Bus monitor, rule evaluation, link to meetings | Example patterns (German, English), docs |
| #13 Wayland | – (Sonnet reviews) | GNOME extension and its install in `hat setup`, KWin script, sway and Hyprland IPC | Recorded-event fixtures, docs |

Rough share of the work: Opus about a fifth (the parts everything else
stands on), Sonnet about half, Haiku the rest.

## Not in this epic

- A browser extension for URLs, shell hooks and git hooks (commit, push):
  strong signals, but each a separate install in every browser, shell or
  repository; possible later. The working directory and branch come from
  `/proc` instead (1a), which needs no install.
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
- Parallel work and the end of a task (2026-11-08, 6a): condensing per hour;
  pieces under 5 minutes go to the main task, also across hats; tickets of
  one hat and activity worked on in turn become one parallel entry; live 10
  of 15 minutes and a new detail only after the memory of 15 minutes; #9
  after the core of #4 (MVP 9); a day report by phases as a new ticket.
- A `workdir` sensor reads repository and branch from `/proc` and
  `.git/HEAD` (2026-11-08, 1a); session files of AI tools are never read.
- Setup inside work tools (settings, plugins, prompt titles, instructions
  for Claude Code) goes into a toolbox of how-tos, its own ticket next to
  #15 (2026-11-08, 3c).
