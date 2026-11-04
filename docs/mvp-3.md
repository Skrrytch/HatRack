# MVP 3 and outlook

Everything after [MVP 2](mvp-2.md) (which includes the HatTray), collected and sorted by benefit. The first
block is MVP 3; the rest is the outlook and gets re-sorted after MVP 2.

## MVP 3

1. **Idle detection.** Lock/unlock and screensaver become `idle_start` /
   `idle_end` events; idle time is not counted, or counted after asking.
2. **Export to the time recording system** (Kimai or the system used at work),
   based on the Week view's totals.
3. **Daily summary** as a notification at the end of the day and in the web UI.
4. **Standalone mode:** the agent runs the core with a local SQLite file, the
   web UI on `127.0.0.1` and MCP over stdio; migration to a server later.

## Outlook

**High benefit**

- "Don't follow" per device: skip applying the look of switches made
  elsewhere; the log stays shared (see [decisions.md](decisions.md)).
- Git identity per hat (`includeIf gitdir` or `GIT_*` variables).
- Firefox profile per hat.
- Calendar entries as activity suggestions.

**Medium benefit**

- Suggestions from the active window ("you are in the VENOM repo, wear VENOM?").
- Window modules: move or close windows on leave (X11, KWin, sway/hyprland).
- An `activity_change` hook for modules (MVP 1 hooks run on hat changes only).
- More backends: XFCE, MATE, sway, Hyprland.
- Global shortcuts through the XDG GlobalShortcuts portal.
- Home Assistant scenes per hat via MQTT.

**Low benefit or later**

- SSH keys and VPN per hat.
- Focus mode, pause reminders, Pomodoro.
- Multiple users, public access to the server.
