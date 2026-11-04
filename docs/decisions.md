# Decisions

## Made

| Date | Decision |
|---|---|
| 2026-11-04 | Name **HatRack**; command `hat`, tray **HatTray**, server **HatRack Server**. PyPI `hat` is taken, so the package is `hatrack` and installs the command `hat`. |
| 2026-11-04 | Client/server split with an optional server; the core is a library so a standalone mode can follow without a rewrite. |
| 2026-11-04 | Modules with per-desktop backends; no dependency on one distribution, display server or UI toolkit. |
| 2026-11-04 | Commits carry no AI attribution (see `CLAUDE.md`). |

## Open

1. **One state for all devices, or one per device?** Proposal: time tracking
   knows exactly one active hat per user; a switch on one device switches all
   others, unless a device is set to "don't follow".
2. **Server hosting** on the home server: name, port, LAN only or public,
   backup of the time data, data location.
3. **Language of the user interface:** English base with German translation
   (like PocketTray)?
4. **Licence and public GitHub repository.**
