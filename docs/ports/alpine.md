# Alpine 3.24 (apk) — port notes

Tested by port-b on 2026-09-25 in the Termux-like Docker box (see `arch.md` for the setup), aarch64, through `families/apk.sh`.

## Rootfs

- `docker.io/library/alpine:3.24`; platforms amd64, 386, arm64v8, armv7, armv6 (+ ppc64le, riscv64, s390x).
- aarch64 export: 3.2 MB `.tar.xz`. busybox only: `/bin/sh` is ash, no bash; bash comes from `DISTRO_BASE_PKGS` (the VNC helpers and the login welcome need it).
- musl libc; apk-tools **3.0.8** (not 2.x). Repos: `https://dl-cdn.alpinelinux.org/alpine/v3.24/{main,community}` (Fastly CDN), fine as is.

## Required fixup: apk scripts and memfd under proot

- apk 3 writes each package script (install scripts **and triggers**) to a `memfd_create(MFD_EXEC)` and runs it with `execve("/proc/self/fd/N")` (`apk-tools src/package.c`, `database.c: apk_db_run_script`). Under proot that exec fails and apk **silently** carries on, printing `Executing glib-2.88.1-r1.trigger` as if it worked.
- Effect: no `gschemas.compiled`, no gdk-pixbuf `loaders.cache`, no mime/icon caches. XFCE starts, every app logs `Cannot get the default GSettingsSchemaSource`, at-spi dies with SIGTRAP and xfce4-session exits within 3 s (black screen).
- apk only uses the memfd when `/proc/sys/vm/memfd_noexec` reads `0` or `1`; with `2` (or no such file, kernels < 6.3) it writes scripts to `/lib/apk/exec/` and they run fine. Fix: bind a file containing `2` over it on every launch:
  ```sh
  echo 2 > ~/.andronix/distros/alpine/sysdata/memfd_noexec
  proot ... -b ~/.andronix/distros/alpine/sysdata/memfd_noexec:/proc/sys/vm/memfd_noexec ...
  ```
  In the bash prototype `fam_configure` writes the file and `lib/launch.sh` binds it when present. Verified: `apk fix glib` then produces `gschemas.compiled`, and a clean install gets a working XFCE. The bind is needed from the first `apk` run on (packages installed without it need `apk fix <pkg>`).
- apk 3 has `--force-no-chroot` but it doesn't help (root is `/` already; the problem is the memfd exec).

## Commands (inside the distro, `sh -c`)

```sh
APK="apk --no-progress"
$APK update                                                     # refresh
$APK upgrade --available --simulate | grep -cE '^\([0-9]+/[0-9]+\) '   # upgrade count
$APK upgrade --available                                        # upgrade
$APK add --simulate PKGS | grep -cE '^\([0-9]+/[0-9]+\) '       # install count
$APK add PKGS                                                   # install
rm -rf /var/cache/apk/*                                         # clean
```

## Progress output (stdout not a tty)

```
(1/3) Installing ncurses-terminfo-base (6.6_p20260516-r0)
(2/3) Installing libncursesw (6.6_p20260516-r0)
(3/3) Installing nano (9.2-r0)
Executing busybox-1.37.0-r31.trigger
OK: 9259 KiB in 19 packages
```
Regex `^\([0-9]+/[0-9]+\) ` (Installing / Upgrading / Replacing), **1 event per package**; the `N` in `(i/N)` is also the total, so the count query could be skipped.

## Packages

- Base extras: `bash sudo nano wget ca-certificates tzdata procps shadow` (`shadow` gives `useradd`/`usermod`/`chpasswd` with the usual flags; busybox has only `adduser`/`addgroup`).
- VNC: `tigervnc xauth`. Alpine's tigervnc 1.16.2 has upstream's `vncserver` (display only), `Xvnc`, `vncpasswd`; `guest/vncserver-start` starts Xvnc directly. musl naming: no `tigervnc-standalone-server`/`-tools` split.
- dbus: `dbus` (has `dbus-run-session`) and `dbus-x11` (`dbus-launch`).
- Browser: `firefox` (151; `firefox-esr` 140 also exists).
- XFCE: `xfce4 xfce4-terminal mousepad dbus dbus-x11 adwaita-icon-theme font-dejavu gsettings-desktop-schemas librsvg` → 238 packages, ~40 s. `gsettings-desktop-schemas` and `librsvg` (SVG icons) are not pulled in by `xfce4` but XFCE needs them. There is no `xfce4-whiskermenu-plugin` in 3.24.
- MATE: explicit list, **not** `mate-desktop-environment` (it pulls `mate-screensaver`, which would lock the VNC desktop behind root's unset password, plus `mate-power-manager`, `mate-polkit`).

## User and admin group

Tested 2026-09-25 (user `andy`, uid 1001). Under proot two things differ from a real system:
- **Log in with `proot -i UID:GID`, not `-0` + `su`.** After `su` from fake root, sudo sees files owned by the emulated uid and refuses. With `-i` the tested sudo versions work, except where noted.
- **Grant sudo by user name, not `%wheel`.** The process's supplementary groups come from the host (Termux's gids), so the guest's `wheel` membership is invisible to sudo (`andy is not in the sudoers file`). `usermod -aG wheel` is still fine for other tools.

```sh
useradd -m -G wheel -s /bin/bash NAME
echo 'NAME:PASSWORD' | chpasswd
echo 'NAME ALL=(ALL:ALL) ALL' > /etc/sudoers.d/NAME && chmod 440 /etc/sudoers.d/NAME
# then log in with: proot ... -i UID:GID ... (instead of -0)
```
- Result: **works** with `sudo -S` and a password (sudo 1.9.17p2). Needs `shadow` for `useradd`/`chpasswd`, already in `DISTRO_BASE_PKGS`.

## Errors and name lookups (card #398, 2.1.6)

apk-tools 3 reports a source it can't fetch as a warning and exits with the number of failed sources, so both sources failing is "exit status 2" and nothing starts with `ERROR`:

```
WARNING: updating https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/APKINDEX.tar.gz: DNS: transient error (try again later)
WARNING: updating https://dl-cdn.alpinelinux.org/alpine/v3.24/community/aarch64/APKINDEX.tar.gz: DNS: transient error (try again later)
0 unavailable, 2 stale; 28581 distinct packages available
```

(`updating and opening` when there is no index yet; `ERROR: <package>: <why>` for a package download.) The reasons, word for word from `libapk.so.3.0.0`, and their kinds (`internal/pkgmgr/diag.go`):

| apk says | kind |
|---|---|
| `DNS: transient error (try again later)`, `DNS: non-recoverable failure`, `DNS: name does not exist`, `DNS: no address for host`, `DNS: address family for host not supported` | `network_dns` |
| `Connection refused`, `Host is unreachable`, `operation timed out`, `could not connect to server (check repositories file)`, `network error (check Internet connection and firewall)`, `network connection aborted`, `temporary error (try again later)`, `TLS: handshake failed (client cert needed?)`, `TLS: server certificate not trusted`, `TLS: server hostname mismatch`, `TLS: unspecified error` | `network_connect` |
| `HTTP 404: Not Found` | `http_404` |
| `HTTP 500: ...` to `HTTP 504: ...` | `http_5xx` |
| `UNTRUSTED signature`, `BAD signature` | `gpg` |
| `file integrity error` | `hash_mismatch` |
| `Unable to lock database` | `lock` |
| `Error relocating apk: <symbol>: symbol not found`, `Error loading shared library ... (needed by /sbin/apk)` (musl's loader failing to start apk itself; 32-bit Termux on kernel 3.18, as card #377) | `guest_libs` |

apk goes on with the other packages after one fails, so in a big install the `ERROR:` line can be hundreds of lines before the end; the installer keeps apk's error lines (the last 8) next to the usual last 60 lines. A network failure is tried again after a pause: 2 runs for anything but the refresh (every package left waits out its own timeout, so one run on a dead network is long already), and for the refresh up to 4 of one kind when the failure is quick (a name lookup, an HTTP 5xx), 3 when the servers can't be connected to, and 5 runs in all.

apk's `--timeout` (60 s by default) is the wait without progress for **each address of each source**. A refresh has two sources and `dl-cdn.alpinelinux.org` has four A records (and four AAAA), so one run on a network that resolves names and drops everything else took 8 minutes, and a connect failure ("Operation timed out", "could not connect to server") got four runs, in 2.1.5 too: over half an hour. Since 2.1.6 the installer's refresh starts with `apk --timeout 15` (`apkRefreshTimeout`). The option also counts a stall in the middle of a download and a slow first byte, so a short timeout by itself would fail slow links that 60 s let through; it is kept only while the network looks dead:

- A run that fails after the server answered (an HTTP error, `I/O error` from a stalled download): the next runs have apk's own timeout.
- A run that couldn't connect: the installer asks the sources' hosts for a TCP connection itself (`apkServersAnswer`, the installer's dialer, up to 15 s). One accepts: a slow link, and the next run has apk's own timeout. None does: one more run, with `--timeout 5` (`apkRetryTimeout`), and not even that when 200 s have passed since the refresh began by the time the first run has failed and the servers were asked (`apkRefreshBudget`).

The short first run isn't counted when the link turned out to be there: the failure then gets the runs 2.1.5 gave it (four for a connect failure or a 5xx, two for one it has no name for), all with apk's own timeout. A slow link is so never worse off than on 2.1.5; a server that accepts connections and never delivers takes one short run longer to fail than it did.

A dead network with four addresses: 120 s, the question (15 s), a 5 s pause, 40 s: 3 minutes. With eight addresses that all drop (IPv6 too): one run and the question, 4 minutes 15. The worst is six: 4 minutes 20. A network that comes and goes can take longer (a run with apk's own timeout in between). Measured with apk-tools 3.0.8 (the only apk the installer has shipped) against addresses that drop everything: one address 120 s by default, 40 s at `--timeout 20`, 10 s at 5; four addresses 120 s at 15, 40 s at 5. The option goes before the command. The refreshes without the retry code (`andronix pack add`, the built-in display's Xvfb, the app's sound) run apk as before.

Timing tells them apart in old telemetry: musl waits 5 s for resolv.conf's servers, so a refresh with dead DNS takes 10 s (two sources); apk's connection timeout is 60 s for each address of each source (so 120 s with one address, 8 minutes with dl-cdn's four).

**Name lookups.** musl has no NSS, so the session's DNS forwarder (card #141, `internal/app/dnsfwd.go`) can't serve Alpine, and resolv.conf takes addresses on port 53 only, which an Android app can't listen on (`ip_unprivileged_port_start` is 1024). Alpine therefore asks only resolv.conf's public servers (8.8.8.8, 1.1.1.1). On a network that doesn't answer those, while Android's own resolver works, every lookup inside Alpine fails.

For the package servers the installer works around it (`internal/app/dnshosts.go`): when a package run fails with a DNS error, it looks the hosts of `/etc/apk/repositories` up through Android's resolver and writes them to the distro's `/etc/hosts` (IPv4 when there is any, at most 4 addresses per host), each line ending in `# andronix: package server, from Android's DNS, for this run only`, and tries again at once. The lines are removed when the install or update ends; what a killed run left is removed by the next run's first package command and at the next proot start (a login, a desktop) that finds no install or update of Alpine running (the distro's lock, `distrolock.go`: a login in another session must not take a live install's lines). The file keeps its permissions; a hosts file that is a link is left alone. A run saved this way has `error_kind` `network_dns_android_retried` in telemetry. `ANDRONIX_DNS=go` turns it off, as it does the forwarder; a build without Android's resolver (`NO_NDK`) doesn't do it either.

Not covered: other names inside Alpine (a browser, `apk add` typed by the user) on such a network. That needs the network's own DNS servers in resolv.conf (the app can read them, the installer can't) or a getaddrinfo shim for musl.

## Quirks

- `DISTRO_SHELL=/bin/bash` only exists after the base packages are in; a half-finished install that stopped before that can't `start` (Go side: fall back to `/bin/sh` when the shell is missing).
- The old Andronix Alpine installer used Xvfb + x11vnc with password "andronix"; the new one uses TigerVNC like the other distros.

## Firefox

- `firefox` 151.0.3 from the distro's own repo (Mozilla's own repo serves .deb/.rpm only; these distros package Firefox themselves, no snap involved).
- Under proot the content sandbox fails: pages render with `[GFX1]: no fonts` and `--headless --screenshot` writes nothing. With `MOZ_DISABLE_CONTENT_SANDBOX=1` it renders (`docs/screens/alpine-firefox.png`, headless, example.com). `guest/xstartup` and `guest/profile.sh` now export it.
