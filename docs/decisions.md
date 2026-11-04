# Decisions

## Made

| Date | Decision |
|---|---|
| 2026-11-04 | Name **HatRack**; command `hat`, tray **HatTray**, server **HatRack Server**. |
| 2026-11-04 | Client/server split with an optional server; the core is a library so a standalone mode can follow without a rewrite. |
| 2026-11-04 | Modules with per-desktop backends; no dependency on one distribution, display server or UI toolkit. |
| 2026-11-04 | Commits carry no AI attribution (see `CLAUDE.md`). |
| 2026-11-04 | The MVP comes in three stages: [MVP 1](mvp-1.md), [MVP 2](mvp-2.md), [MVP 3 and outlook](mvp-3.md). |
| 2026-11-04 | Configuration (hats, activities, module settings) can be exported and imported as YAML, on the server and locally, so a setup moves with one export and one import. |
| 2026-11-04 | Activities are optional. Per hat: `resume_activity: never \| last \| ask`. Unknown activities are rejected. |
| 2026-11-04 | Hat and activity names ignore case and accept unique prefixes. |
| 2026-11-04 | `hat off` is a pause: the look stays, only the clock stops. `hat off --neutral` restores the neutral look. |
| 2026-11-04 | No `hat menu` for now. HatTray and `hat tray run\|show\|hide\|toggle` first planned for MVP 3, moved to MVP 2 (see below). |
| 2026-11-04 | One state per user: a switch on any device switches all devices, and the log never has overlapping intervals. Rejected: one state per device (overlaps, every report needs a rule which device counts; high effort, little benefit for one user). A per-device "don't follow" flag that only skips applying the look can come later without changing the model (see [MVP 3 and outlook](mvp-3.md)). |
| 2026-11-04 | Licence MIT, as for OTP-Scout. Public repository on GitHub. |
| 2026-11-04 | Go instead of Python: one static binary `hat` for CLI, agent, server and tray, no runtime to install, independent of the distribution. Web UI with `templ` + HTMX, tray with `fyne.io/systray` (StatusNotifierItem over D-Bus, no GTK/Qt), D-Bus with `godbus/dbus`, SQLite with `modernc.org/sqlite`, MCP with the official Go SDK. External modules are executables speaking JSON over stdin/stdout. Rejected: Rust (slower development for a glue-heavy tool), TypeScript (needs a runtime, weak D-Bus and tray support). |
| 2026-11-04 | User interface in English and German. The web UI picks the language from the browser (`Accept-Language`) and can be switched; CLI, tray and notifications follow the locale (`LC_MESSAGES`/`LANG`) and can be overridden with `language: auto \| en \| de`. All texts come from message catalogs from MVP 1 on. |
| 2026-11-04 | The activity is always introduced by `do`: `hat on venom do review` mirrors `hat do review` for the current hat. Longer, but one pattern to remember. |
| 2026-11-04 | State transitions: `do` and `off` without a worn hat are errors that write no event. `hat on` / `hat on X` resumes a paused hat with a `switch` event. A backdated event (`--at`) changes the look only if it is the newest event. See [MVP 1](mvp-1.md). |
| 2026-11-04 | Hats are identified by `key`, activities by name within the hat. Renaming a key or an activity creates a new one; history stays with the old one. |
| 2026-11-04 | MVP 1 needs a server (local on `127.0.0.1` is fine); the agent caches the configuration for outages. Standalone mode stays in MVP 3. |
| 2026-11-04 | MVP 1 deviations from the first plan: the "Now" page uses `html/template` with a meta refresh instead of `templ` + HTMX (one read-only page does not justify a generator and a script; both come with the full web UI in MVP 2); messages come from a small embedded catalog (`internal/i18n`) instead of `go-i18n` (only `%s` placeholders are needed so far); the module is called `hatrack`; Go 1.24 or newer (`omitzero`). |
| 2026-11-04 | The "Now" page needs a device token, given once as `/?token=…` and kept in a cookie, so a server on the network does not show what you are working on. A real login follows with the web UI in MVP 2. |
| 2026-11-04 | Events refer to hats by key and to activities by name, not by an internal id. Keys are stable by design, and the log stays readable on its own. |
| 2026-11-04 | `hat on X` while X is already worn changes nothing (it would otherwise reset the activity); `hat on X do Y` changes the activity. |
| 2026-11-04 | After MVP 1 was built: the command line is the base, but the web UI and the HatTray are the tools used every day. HatTray moves from MVP 3 to MVP 2. MVP 2 is built in slices (read-only web UI, HatTray, Setup forms, corrections, the rest), each usable on its own. |
| 2026-11-04 | Web UI login: one user per server, a password only. It is set with `hat server user set-password` (prompt, or one line from stdin). The password is stored as a bcrypt hash, sessions are random tokens (30 days, only their hash is stored, cookie `HttpOnly` and `SameSite=Strict`), a changed password ends all sessions. Posts also check the `Origin`. Failed logins are slowed down. A device token in `?token=` still gives a read-only view. |
| 2026-11-04 | The web UI stays on `html/template` with a few lines of plain JavaScript (time zone cookie, reload on change) instead of `templ` + HTMX: pages are whole documents, forms work without scripts, and there is no generator and no third-party script to vendor. Times are shown in the browser's time zone (cookie), else the server's (`--timezone`). |
| 2026-11-04 | In the web UI, `resume_activity: ask` counts as `never`: the activity buttons are right next to the hat. |
| 2026-11-04 | HatTray: `hat tray hide` ends the tray process and `show` starts it, because `fyne.io/systray` cannot hide the icon of a running process on Linux. Clicking a hat is `hat on <hat>`, so its `resume_activity` applies; "Hat only" in the submenu starts it without an activity. |
| 2026-11-04 | Icon: a dark rounded square with a frame in the hat's colour and a cream top hat whose band has the hat's colour; grey (`#8a8f98`) while paused or without a hat. Designed by Bert, drawn in code by `internal/icon` for the tray and used as `hatrack.svg` (blue) for the web UI favicon. |
| 2026-11-04 | Panel (quick switcher) instead of a long tray menu: a left click on the tray icon or `hat panel` (keyboard shortcut) opens a small window with all hats and activities; right click gives a slim menu (open, pause/resume, about, quit). Technique: own X11 window in pure Go (no toolkit, no cgo, embedded Go fonts, software drawing), Wayland later as another backend. Icon code moved to `internal/icon`. |
| 2026-11-04 | First-time setup is one interactive command, `hat setup`: it asks where the server runs (here by default, else address and token), installs server and agent as user services, creates this computer's device token itself (no copying for a local server), asks for the web password, offers two starter hats and sets up the tray. It can be run again; flags (`--server`, `--token`, `--yes`, `--no-services`, `--no-tray`) make it scriptable. A setup page in the web UI can later build on the same steps. |
| 2026-11-04 | Module settings are checked per hat together with the defaults they extend: a hat may set only what differs (e.g. the wallpaper fit), and defaults may lack a required value as long as every active hat ends up with one. |
| 2026-11-04 | Renaming an activity in Setup renames it from then on; the time log keeps the old name for past entries, because events store activity names as text. Hats are renamed by their display name only; the key stays and the log follows. |
| 2026-11-04 | The server reports an event revision (`event_rev`, growing with every stored event) with each state and in the answer to `POST /api/events`. An agent ignores states older than its last sent events, so a late state message cannot undo a switch. |

## Open

- **Cinnamon `color_scheme`:** does Cinnamon evaluate a light/dark preference,
  or only the GTK theme? Test on the desktop early in MVP 1; if not, drop
  `prefer`.
