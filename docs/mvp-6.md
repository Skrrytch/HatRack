# MVP 6: Installing without the source code

Until now HatRack is installed from a checkout with `scripts/update.sh`, which
needs Go. MVP 6 makes it installable on any Linux computer from a release, and
keeps the setup as short as it is today. Decision: see
[decisions.md](decisions.md) (2026-11-05, release artifacts). Five slices, each
usable on its own:

1. **Release artifacts:** tarball with `install.sh`, `.deb`, container image.
2. **Updates:** `hat update`, self-restart after a new binary, version check.
3. **Server in a container:** `deploy/compose.yaml`, not a setup option.
4. **README:** installation in a few lines, each with its reason.
5. **Saying what works on which desktop.**

`scripts/update.sh` stays the developer path.

## Status

- **Released:** `v0.6.0`, the first stable version with these files.
- **Done, tested locally:** self-restart of agent, server and tray
  (`internal/reexec`); version and protocol in the hello, a mismatch shown in
  `hat status` (`internal/agent`, `internal/server`); `hat update`
  (`internal/cli/updatecmd.go`, `internal/release`); `hat setup --remove` and
  `--purge`; desktop notes in `hat setup`; `install.sh` (checked against an
  unpacked tarball without a terminal); README install section.
- **Released as a pre-release:** `v0.6.0-rc1` (tarballs, `.deb`, image for
  amd64 and arm64, checksums). The installer was checked against the real
  files. The fix after it (pre-releases marked automatically, `:latest` only
  for stable tags) is tested by the next tag.
- **Not done on purpose:** a container option in `hat setup` (slice 3).

## 1. Release artifacts

One tag (`v0.6.0`) builds everything in GitHub Actions with GoReleaser, with
the same version in every artifact and a `checksums.txt`:

| Artifact | Installs | For |
|---|---|---|
| `hatrack_<v>_linux_<arch>.tar.gz` (amd64, arm64) | `hat`, `install.sh`, README, licence | any distribution, no root |
| `hatrack_<v>_<arch>.deb` | `/usr/bin/hat`, menu entry, icon | Debian, Ubuntu, Linux Mint |
| `ghcr.io/skrrytch/hatrack:<v>` and `:latest` (amd64, arm64) | the server, as `deploy/Dockerfile` builds it today | NAS, home server, or locally in Docker/Podman |

- **`install.sh`** works from the unpacked tarball or as
  `curl -fsSL …/install.sh | sh`. It picks the architecture, downloads and
  checks the checksum (when not run from a tarball), copies `hat` to
  `~/.local/bin`, warns if that is not on `PATH`, and then runs `hat setup`
  when a terminal is attached. Nothing else: everything that has to do with
  services lives in `hat setup`, so all three ways end in the same place.
- **`.deb`:** no dependencies (static binary). `Recommends: libglib2.0-bin`
  (gsettings), `xdg-utils`; `Suggests: gnome-shell-extension-appindicator`.
  The package touches no user's files: `postinst` only says to run
  `hat setup`, removing the package keeps all data. The menu entry starts
  the tray (`hat tray show`).
- **Image:** published as is. A `deploy/compose.yaml` next to the Dockerfile
  (port only on `127.0.0.1`, named volume, `.env` for secrets) replaces the
  `docker run` lines.
- **Removing** must be as short as installing: `hat setup --remove` stops and
  removes the services and the tray autostart and restores the neutral look;
  the data stays unless `--purge` is given.

## 2. Updates

- **`hat update`** for tarball installs: asks GitHub for the latest release,
  checks the checksum, replaces the binary (same `mv` as `update.sh`) and
  restarts the services and the tray. For a binary below `/usr` (the `.deb`)
  it does not replace anything but names the newer `.deb` to install.
  `--check` only says whether there is a newer version.
- **Self-restart.** Installing a new `.deb` cannot restart user services. So agent,
  server and tray check their own executable now and then (inode and mtime
  of `os.Executable()`); when it was replaced they exit, and systemd
  (agent, server) or the agent (tray) starts them again with the new one.
  This also covers processes `update.sh` cannot restart today.
- **Version in the hello.** Agent and server send their version. A different
  version is fine as long as the protocol matches; a protocol mismatch shows
  in `hat status`, the tray and the web UI with what to update. Needed
  because a container server is updated on its own (`compose pull`).

## 3. Server in a container

`hat setup` keeps its two answers: the server runs here as a service
(default), or elsewhere (address and token). A container is the "elsewhere":
`deploy/compose.yaml` starts it, `docker compose exec hatrack /hat server
device add <name>` prints a token, and `hat setup --server … --token …`
connects. The README says so in four lines.

Why no setup option: a third answer needs Docker/Podman detection, generated
compose or Quadlet files and a token created inside the container. That is
much code for a path few take, and it makes the one command that should stay
short longer. Decision: [decisions.md](decisions.md) (2026-11-05).

Possible later, and small: device tokens from the web UI (Setup → Devices), so
the `docker compose exec` line is not needed.

## 4. README

The README is where people decide whether to try HatRack, and the
installation section is most of that decision. Rules:

- **What and why, one line each.** Every step says what to do and, in half a
  sentence, why it is done that way. No step without a reason, no reason
  longer than the step.
- **Short means honest.** It is one binary and one setup command; the text
  must not look bigger than that. No option tables, no troubleshooting
  sections in the README: options are in `hat <command> --help`, details in
  `docs/`.
- **One way first.** The tarball is the way that works everywhere and is shown
  first; `.deb` and the container are one short paragraph each.
- **Removal is part of installation.** Showing how to remove it in one line
  says more about how light it is than any claim.
- **Say what works.** One line on supported desktops (slice 5), before the
  install steps, so nobody installs and then wonders.
- The developer path (`update.sh`) moves to a short "Build from source"
  section at the end.

Target shape of the section (wording final when the slices exist):

```markdown
## Install

Works fully on Cinnamon (X11). On other desktops the time log, apps and
notifications work; the look does not change yet.

    curl -fsSL https://github.com/Skrrytch/HatRack/releases/latest/download/install.sh | sh

This puts the single binary `hat` into `~/.local/bin` (no root, nothing
system-wide) and starts `hat setup`, which asks four questions: where the
server runs, a web password, starter hats, tray yes or no.

Why a server at all? It keeps the time log and serves the web UI. By default
it runs on this computer as a user service, so there is nothing to host.

- **Debian, Ubuntu, Mint:** install the `.deb` from the releases instead if
  you want it system-wide with a menu entry, then run `hat setup`.
- **Server in a container** (to share one log across computers): choose it in
  `hat setup`, or use `deploy/compose.yaml` on another machine.

Update with `hat update` (for the `.deb`: install the newer one). Remove with `hat setup --remove`; your
log stays in `~/.local/share/hatrack` until you delete it.
```

## 5. Saying what works

The look modules have only a Cinnamon backend; the panel only an X11 one.
Instead of failing quietly:

- `hat setup` names the desktop it found and, outside Cinnamon on X11, says
  in one line what works and what does not yet.
- On GNOME it checks for a StatusNotifier watcher and, if missing, names the
  AppIndicator extension.
- The same line is in the README (slice 4).

Backends for further desktops (GNOME and MATE via gsettings, Xfce via
`xfconf-query`, KDE via `plasma-apply-*`) and the panel on Wayland are
separate work, not part of MVP 6.

## Not in MVP 6

- rpm, AUR, Flatpak, Snap, AppImage (see the decision).
- An apt repository; the `.deb` is downloaded from the releases, so `apt`
  updates it only when installed again. A repository can follow.
- Signed releases beyond checksums.
- Automatic updates without asking.
