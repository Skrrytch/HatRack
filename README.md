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
hat on venom review     # ... with an activity
hat do meeting          # change the activity, keep the hat
hat off                 # take the hat off (pause)
hat                     # what am I wearing, since when
hat log today           # today's overview
```

> **Status:** concept. Nothing is built yet. See [docs/concept.md](docs/concept.md)
> and the open questions in [docs/decisions.md](docs/decisions.md).

## Components

| Name | What | Package / command |
|---|---|---|
| **HatRack** | the tool as a whole | `hatrack` |
| **hat** | command line and background agent | `hat` |
| **HatTray** | tray icon and menu | `hattray` |
| **HatRack Server** | optional central server: web UI, API, MCP for AI tools | `hatrack-server` |
| *core* | shared library, not used directly | `hatrack-core` |

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
