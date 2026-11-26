# Arch Linux (pacman) — port notes

Tested by port-b on 2026-09-25 in the Termux-like Docker box (`tests/Dockerfile`: Debian 13 arm64, non-root user, Termux's proot fork with `--link2symlink --kill-on-exit --sysvipc -L`), aarch64 only. Everything below ran through the bash prototype (`families/pacman.sh`), so the commands are exactly what was executed. Manjaro shares all of this; see `manjaro.md` for its differences.

## Rootfs

| CPU | Source |
|---|---|
| x86_64 | `docker.io/library/archlinux:latest` (the only platform that image has) |
| aarch64 | `http://os.archlinuxarm.org/os/ArchLinuxARM-aarch64-latest.tar.gz` (829 MB gzip, has a `.md5`) |
| arm | `http://os.archlinuxarm.org/os/ArchLinuxARM-armv7-latest.tar.gz` (not built or tested) |
| i686 | none (Arch dropped it) |

- `ci/build-rootfs.sh arch aarch64` downloads the tarball, checks the `.md5`, `docker import`s it, then removes kernel, firmware and network tools and exports:
  ```sh
  pkgs=$(pacman -Qq linux-aarch64 linux-armv7 linux-firmware dhcpcd netctl openssh net-tools 2>/dev/null)
  pacman -Rns --noconfirm $pkgs        # only installed names, or pacman refuses the transaction
  rm -rf /boot/* /var/cache/pacman/pkg/* /var/lib/pacman/sync/*
  ```
  The removal needs no network and no keyring. Result: 138 MB `.tar.xz` (from 1.9 GB `/usr`, of which 960 MB firmware and 188 MB modules).
- There is no OCI image for ARM, so **no registry fallback** on aarch64/arm: the CI tarball must be published. (Alternative: push our export to `ghcr.io/andronixapp/arch` and point the fallback there.)
- Conf keys added for this (documented in DESIGN.md): `DISTRO_UPSTREAM_TARBALL_<arch>`, `DISTRO_UPSTREAM_REMOVE`.
- The ALARM tarball ships an empty `/etc/machine-id` and **no initialised keyring** (`/etc/pacman.d/gnupg` missing; the keyring *packages* are there, see "Keyring" below). pacman 7.1.0, glibc 2.43 (Aug 2026 build).

## Host-side fixups (before first pacman run)

Edit `/etc/pacman.conf`:
```sh
sed -i 's/^CheckSpace/#CheckSpace/' /etc/pacman.conf
# pacman >= 7 only (check /var/lib/pacman/local/pacman-<ver>): add under [options]
DisableSandbox
```
- Both are **precautions**: in Docker, `CheckSpace` and pacman 7's sandbox (Landlock + `DownloadUser = alpm`) both worked under proot. On Android, `/proc/mounts` entries can be unreadable, and Android's app seccomp policy may not allow Landlock syscalls; untested on a phone.
- pacman 6 prints `warning: config file /etc/pacman.conf, line N: directive 'DisableSandbox' in section 'options' not recognized.` on **every** command, so only add it for 7+ (Manjaro ARM stable is on 6.0.2).
- Mirror: keep the image's `Server = http://mirror.archlinuxarm.org/$arch/$repo` (a GeoIP redirector). Arch x86_64 image: its mirrorlist is fine.

## Commands (inside the distro, `sh -c`)

```sh
PAC="pacman --noconfirm --noprogressbar --color never"
# keyring, until it works (about 10 s on the Mac; ~6 keys signed, 38 disabled)
# (first: rm -f /etc/pacman.d/gnupg/*.lock /etc/pacman.d/gnupg/.#lk* when no gpg program runs)
gpg --homedir /etc/pacman.d/gnupg --batch --no-permission-warning --list-keys --with-colons 2>/dev/null | grep -q '^pub:f:' \
  || { pacman-key --init && pacman-key --populate; }
$PAC -Syy                                   # refresh
# reinstall packages a killed pacman left half-installed (see "Interrupted runs")
# keyring packages first, then the upgrade (DISTRO_PRE_UPGRADE; best-effort, no keyserver)
$PAC -Sy --needed archlinux-keyring archlinuxarm-keyring; pacman-key --populate
pacman -Qu | grep -c .                      # upgrade count (NOT with --noprogressbar: "invalid option")
$PAC -Su                                    # upgrade
$PAC -Sp --needed --print-format %n PKGS | grep -vc '^:: '   # install count (groups expand)
$PAC -S --needed PKGS                       # install
rm -f /var/cache/pacman/pkg/*               # clean
```
- `pacman-key --populate` with no argument populates every keyring in `/usr/share/pacman/keyrings` (archlinuxarm here).
- Don't use `-Sy PKG` (partial upgrade); refresh, `-Su`, then `-S`. **One exception, Arch's own:** the keyring packages go first (`pacman -Sy --needed archlinux-keyring archlinuxarm-keyring`, then `-Su`), so packages signed by keys newer than the image's keyring verify. It is best-effort and never fails the install.
- **No keyservers.** Nothing here runs `pacman-key --refresh-keys` or `--recv-keys`: under proot on a phone it took up to three minutes whenever the mirror was slow and fixed nothing. If the image's keyring is too old to verify the new keyring package, the fix is a new image (`ci/build-rootfs.sh arch <arch>`).

## Keyring (which packages, per CPU)

Checked 2026-11-11 on our published images (`dl.andronix.app/rootfs/arch/rolling/`, both built 2026-09-26):

| Image | pacman | Keyring packages in the image | Signs the repo's packages |
|---|---|---|---|
| aarch64 (`ArchLinuxARM-aarch64`) | 7.1.0 | `archlinux-keyring` 20260727-1, `archlinuxarm-keyring` 20240419-2 | Arch Linux ARM Build System `68B3537F39A313B3E574D06777193F152BDBE6A6` (in `archlinuxarm-keyring`; no expiry date) |
| arm (`ArchLinuxARM-armv7`, not shipped) | same repo layout | same two packages | same key |
| x86_64 (`archlinux:latest`, registry only, no tarball of ours) | Arch's current | `archlinux-keyring` only (no `archlinuxarm-keyring` in Arch's repos) | Arch packager keys, signed by the Arch master keys |

- On ARM every package, `archlinux-keyring` included, is signed by the one Arch Linux ARM build key. `archlinuxarm-keyring` has not changed since April 2024, so the image's keyring verifies today's repo: a fresh image upgrades all 65 pending packages with the keyring it ships (tested 2026-11-11, arm64 container). The keyring is **not** stale.
- `DISTRO_PRE_UPGRADE` asks `pacman -Si` for each name and only installs the ones the repo has, so one line serves all CPUs and Manjaro.

## Interrupted runs (support #5349, #5412)

Both of these are a run that was stopped half-way (the user cancelled, or Android closed the app) and then started again. Every later run failed the same way until the distro was removed.

1. **Stopped during `pacman-key --init` / `--populate`** (the "Refreshing package lists" step). `pubring.gpg` then exists with a master key, or with imported keys nothing has signed. The old test (`[ -s pubring.gpg ]`) took that for a finished keyring and skipped `--populate` for good; `-Su` then downloaded everything and failed each package:
   ```
   error: nftables: signature from "Arch Linux ARM Build System <builder@archlinuxarm.org>" is unknown trust
   :: File /var/cache/pacman/pkg/nftables-1:1.1.7-3-aarch64.pkg.tar.xz is corrupted (invalid or corrupted package (PGP signature)).
   error: failed to commit transaction (invalid or corrupted package (PGP signature))
   ```
   Now the keyring counts as ready only when gpg lists a fully trusted key (`pub:f:`); otherwise both `pacman-key` steps run again. They are safe to repeat: `--init` only makes a master key when there is none, `--populate` re-imports and re-signs. A package that still fails its signature gets the same rebuild, once, before the retry.

   One trusted key doesn't prove `--populate` finished (it signs the keys one at a time), so the keyring also needs the stamp `/etc/pacman.d/gnupg/.andronix-keyring-ok`, written only when both steps exit 0. An install made before the stamp (2.1.5 and older) has a finished keyring and no stamp, so its first refresh on 2.1.6 runs both steps once more, offline. On a phone where gpg-agent won't start (support #119) they fail; with a trusted key in the keyring the refresh then goes on to the sync with the keys the install has, prints that it did, writes no stamp, and the next refresh tries again. With no trusted key a failed keyring still stops the refresh. gpg is only asked for trusted keys when the keyring has the stamp or a `trustdb.gpg`: asked about an empty directory it would create `pubring.kbx`, and the keys would then go there instead of `pubring.gpg`. This holds for every caller of the family's `Update`, also the ones without the retry code (`andronix pack add`, the built-in display's Xvfb, the app's sound). The retry code's own keyring recovery (`keyringReset`, then gpg-agent started by us) no longer wipes a keyring that has trusted keys.

   The killed gpg also leaves its lock files in `/etc/pacman.d/gnupg` (`pubring.gpg.lock`, `.#lk*`). gpg removes a stale lock only when it can tell the owner is gone (same host name, pid not in use). When it can't, it waits for good: seen in testing as `pacman -Su` sitting at "checking keyring" for 10 minutes after the container's host name changed; on a phone a reused pid would do the same. So the refresh deletes these locks first, unless a gpg program (`gpg`, `gpg-agent`, `dirmngr`, `keyboxd`, `pacman-key`) is running.
2. **pacman killed while unpacking a package.** pacman removes the old version's entry in `/var/lib/pacman/local`, makes the new one's directory, unpacks, and only then writes `desc` and `files`. Killed in between, the entry has only `mtree` (4 of 14 kills at different moments of a 65-package upgrade left one). pacman then counts the package as installed with half its files missing, and a transaction that touches it stops:
   ```
   error: could not open file /var/lib/pacman/local/vim-runtime-9.2.0849-1/desc: No such file or directory
   warning: could not fully load metadata for package vim-runtime-9.2.0849-1
   error: failed to prepare transaction (invalid or corrupted package)
   ```
   After each refresh the installer looks for entries without `desc` or `files`, removes them and reinstalls those packages (`$PAC -S --overwrite '*' NAMES`; the names are kept in `/var/lib/pacman/andronix-reinstall` until that worked). The same repair runs once when a pacman run fails this way (kind `pacman_db`).

## Progress output (stdout not a tty)

```
:: Retrieving packages...
 sudo-1.9.17.p2-6-aarch64 downloading...
checking keyring...
...
:: Processing package changes...
upgrading tzdata...
installing sudo...
```
Regex used: `^ .*-(any|aarch64|armv7h|x86_64) downloading|^(installing|upgrading|reinstalling) ` with **2 events per package** (download + install). Already-cached packages skip the download line, so the bar can end below 100% — harmless. The `-Sp` count includes group members and dependencies.

## Packages

- Base extras (`DISTRO_BASE_PKGS`): `sudo nano wget ca-certificates tzdata procps-ng which`
- VNC: `tigervnc xorg-xauth`. dbus: `dbus` (has `dbus-run-session` and `dbus-launch`).
- Browser: `firefox`.
- XFCE: `xfce4 xfce4-whiskermenu-plugin mousepad dbus adwaita-icon-theme ttf-dejavu` → 240 packages, 2m40s.
- LXQt / MATE: see `desktops/*.conf` (explicit lists, not the groups).

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
- Result: **works** with `sudo -S` and a password (sudo 1.9.17p2).

## VNC

- Arch's TigerVNC 1.16 `vncserver` is **upstream's**: `usage: vncserver <display>`, options only from `~/.config/tigervnc/config`, runs in the foreground (`exec xinit`), no `-xstartup`, `-geometry` or `-kill`. `guest/vncserver-start` now detects that (no `-xstartup` in the script) and starts Xvnc itself:
  ```sh
  Xvnc :1 -desktop remote-desktop -localhost=0 -rfbport 5901 -rfbauth ~/.config/tigervnc/passwd \
       -SecurityTypes VncAuth -depth 24 [-geometry WxH] &
  # wait for /tmp/.X11-unix/X1, then
  DISPLAY=:1 ~/.config/tigervnc/xstartup &
  ```
  `vncserver-stop` kills the pid in `/tmp/.X1-lock` when `vncserver -kill` fails.
- **dbus:** `dbus-launch --exit-with-session CMD` loses its bus immediately under proot (the watcher kills the daemon; plain `dbus-launch` keeps it). XFCE then shows "Unable to contact settings server". `guest/xstartup` now uses `exec dbus-run-session -- SESSION` when available (every distro here has it).
- LXQt first login asks for a window manager; the installer writes `/root/.config/lxqt/session.conf` with `[General]\nwindow_manager=openbox`.

## Quirks (harmless)

- `warning: warning given when extracting /usr/bin/sudoedit (Can't set permissions to 0777)`: libarchive's lchmod on symlinks. glibc 2.39+ calls `fchmodat2`, which proot doesn't translate (see `void.md`); pacman only warns.
- Hooks print `Skipped: Current root is not booted.` and a systemd-tmpfiles `uninitialized /etc/ detected` line.
- MATE: caja exits and is respawned by mate-session several times in the first ~30-40 s (systemd1 activation fails), then stays up and draws the desktop.

## Firefox

- `firefox` 156.0.1 from the distro's own repo (Mozilla's own repo serves .deb/.rpm only; these distros package Firefox themselves, no snap involved).
- Under proot the content sandbox fails: pages render with `[GFX1]: no fonts` and `--headless --screenshot` writes nothing. With `MOZ_DISABLE_CONTENT_SANDBOX=1` it renders (`docs/screens/arch-firefox.png`, headless, example.com). `guest/xstartup` and `guest/profile.sh` now export it.
