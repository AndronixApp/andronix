# Changelog

Changes to the `andronix` installer and the free distro images it installs. The app, the website and the docs have their own release notes. See https://docs.andronix.app for how to use the installer.

## 2.0.10 (2026-10-15)

2.0.9 was only a release candidate (beta); everything in it ships here too.

- **A phone where a tool inside the distro crashes is no longer told its network is at fault** (support #470, #471): on some phones Termux's Linux layer makes programs in the distro crash (the signature checker gpgv) or fail with "Function not implemented", so every package list looked unsigned and the install said "your network is likely showing a sign-in page". The installer now recognises it, tries once more with proot's fast mode off, and if that doesn't help says to update Termux.
- **One PulseAudio for sound, not one more at every start**: where Termux runs programs through Android's linker, PulseAudio's own check said it wasn't running when it was, so every `andronix start` launched another sound server (a dozen after a dozen starts), each one a process toward Android's limit of 32. The installer now asks the running server's socket first.
- **A desktop that lost one of its own packages gets it back** (support #458): a Kali XFCE install without dbus-x11 (removed since the install) quit at once with "dbus-launch: not found". `andronix update` now puts back any missing packages of the installed desktop, and when the session can't start for that reason its log says which package and to run `andronix update`.
- **Distros with systemd install on kernel 3.10 phones** (support #436, #438): systemd stopped with "Failed to take /etc/passwd lock: Invalid argument", because it locks the user database with a kind of lock Linux added in 3.15, and then every package that needs dbus or polkit stayed unconfigured (Kali; Ubuntu with KDE). On kernels before 3.15 a small shim now gives systemd the older lock instead.
- **The time zone your phone reports is always accepted** (support #443, #435): Android still reports some zones by old names (Asia/Calcutta for India) that newer Ubuntu and Debian keep only in an extra package, so setting up the user, the last step of an install, failed with "Use a name like Europe/Berlin". The installer now maps old names to the current ones (Asia/Kolkata), accepts a name typed in another case, and never fails setup over a time zone: one it can't find keeps the current zone.
- **One install of a distro at a time** (support #313): a second `andronix install` of the same distro in another Termux session (or while the app is installing it) failed on the first one's package lock, while the first finished fine. Install, update, remove and restore now hold a lock per distro, and a second run stops at once with "<Distro> is being installed in another Termux session", saying to wait or close it and run the same command again. The lock goes with its process, so a crashed or killed run never blocks the next.
- **A resumed install waits for a leftover package run** (support #296): when an earlier attempt's apt or pacman is still running under this distro's proot with nothing left to start it, the installer waits up to 5 minutes, then stops that leftover, finishes dpkg's configuration and tries again. A package run that belongs to a live install, or to anything outside this distro, is never touched.
- **Play Store Termux on a network that blocks termux.net says so** (support #305): it read as a missing package. Play Store Termux has only one package server, so the message says your network blocks it and to switch network or use a VPN; other Termux keeps the mirror advice.
- **Fake root that didn't take is retried once** (support #312): on some phones (seen on an Android 16 ROM with kernel 4.14), Arch stopped with "pacman-key needs to be run as root". The install now retries once with proot on ptrace only, and the log says whether that fixed it.
- **Fedora installs on x86 Chromebooks and x86 Android** (support #326): Android's x86 app sandbox refuses the old file-creation system call that Fedora's package manager uses to import its keys ("Function not implemented"). A small shim, on x86 only and only where the check finds the call blocked, swaps it for the modern one. The fix that gives systemd its mount ids now also goes in wherever the check finds them missing, whatever the kernel (ChromeOS on 5.10).
- **Kali gets Mozilla's Firefox** (64-bit; 32-bit phones keep Firefox ESR): Kali moved to FFmpeg 9, which Firefox ESR 140 can't load, so videos with H.264 or AAC didn't play. `andronix update` switches existing Kali installs: it installs Mozilla's Firefox from Mozilla's package repository (as on Ubuntu), keeps your Firefox profile (bookmarks, logins, history), points the dock, the Whisker menu favourites and your launchers at it, and removes Firefox ESR only when nothing else depends on it.

## 2.0.9 (release candidate only; shipped in 2.0.10)

- **A desktop added to a command-line install starts for your user** (support #284): the user made at the first login of a command-line install never got the desktop's session files, so `andronix desktop` stopped with "The desktop session is missing". Installing a desktop, and `andronix desktop`, now copy what the user's home lacks from /etc/skel, never overwriting your files (VNC already did).
- **Andronix works on networks that block public DNS** (card #141): where a network drops DNS to public servers, every install stopped at "Finding the best download" with "Couldn't reach the download servers", while curl and the Andronix app worked. The installer looked names up itself through public servers (Termux's 8.8.8.8, then 1.1.1.1), and so does the distro (its resolv.conf). Now both go through Android's own resolver (the network's DNS, or Private DNS): the installer asks it first, with the public servers as the fallback, and each session runs a small forwarder that glibc distros ask through an NSS module (`hosts: files andronix [NOTFOUND=return] dns` in nsswitch.conf, set again on every start). A name that doesn't exist answers at once; with no forwarder, lookups go to resolv.conf as before. Alpine (musl) keeps resolv.conf only. The install log says which resolver answered ("dns dl.andronix.app: android"). The 64-bit Termux build now links with Android's C library, like the other CPUs. `ANDRONIX_DNS=go` keeps the old behaviour.

## 2.0.8 (2026-10-10)

- **`andronix report` sends the failed run's own log**, also for errors without one of their own; a newer run since (a "Try again") follows it, shorter. Before, such a report carried only the newest log, the retry's (support #255).
- **A question with no keyboard to answer it says so**: when its input isn't a terminal, a prompt fails with "This needs a terminal" and how to run the command instead, and the installer's screens never try to open /dev/tty themselves (support #255 saw "could not open a new TTY: open /dev/tty: no such device or address").
- **sudo no longer prints "unable to send audit message"** (Ubuntu 26.04 and other distros with the classic sudo): under proot, every sudo printed "sudo: unable to send audit message: Operation not permitted" even when it worked, which read as sudo failing (support #253). The installer turns off sudo's audit logging (`Defaults !log_allowed, !log_denied` in `/etc/sudoers.d/andronix-quiet`) when the user is set up, and on `andronix update` and `andronix start` for distros installed before. Not with sudo-rs, which doesn't know those settings.

## 2.0.7 (2026-10-09)

### Desktops

- **A desktop with Firefox uses fewer processes**, so Android (which starts closing apps' background processes past 32, for all apps together) is less likely to stop it. Firefox isolates only high-value sites (logins and the like) in their own process, as Firefox for Android does, with one shared content process and no fork server, and opens a blank home and new-tab page (Firefox's own start page needs a process of its own). XFCE starts without Thunar's background daemon (Thunar still opens when asked) and without the status tray. A desktop with Firefox and 17 tabs is about 29 processes under Termux, down from about 37. Firefox's settings apply on install and on `andronix update` and can be changed in `about:config`; XFCE's apply to new desktops.
- **A runaway X server can't fill the phone any more**: the VNC server's log is capped at 16 MB (the kernel stops the server there), and the desktop session writes its own log. When Android stopped proot, an X server left behind wrote 10 GB of the same error in 20 minutes.

### Package manager

- **A distro's own preparation step gets the usual recoveries**: Ubuntu 26.04's switch to GNU coreutils (an apt install inside "Refreshing package lists"; today's images mostly have it done already) is now diagnosed, retried and logged like every other package step, instead of failing with no log.
- **A failing apt hook no longer stops a refresh**: when none of the known hooks matches, the refresh runs once more without apt's update hooks (for that run only), and the hooks are logged.

### Install

- **An install that was interrupted while a package was unpacking can be finished.** The retry used to stop on "package is in a very bad inconsistent state; it should be reinstalled" every time; now it reinstalls the half-installed packages (removing any dpkg still refuses), finishes configuring the rest and tries again.

- **Kali gets through when its download server redirects in circles.** Kali's kali.download now sends package indexes to http.kali.org, which can send them back ("Redirection loop encountered"). Kali now uses http.kali.org, Kali's own redirector to a nearby mirror, as its image does. When a mirror still loops or answers without its package index ("does not have a Release file"), the installer moves once to a direct mirror; if that fails too, it says your network or region is likely redirecting the package servers. An index sent to an https mirror before the certificates are installed is retried as a connection error.
- **Kali (and other distros with systemd 256 or newer) installs on 32-bit phones with kernels older than 5.8.** The fix that gives systemd the mount ids these kernels don't report now goes in by the kernel version alone. Before, it waited for a check inside the distro that Termux's proot can't run on 32-bit phones, so it never went in there and Kali stopped at "Installing the basics" (`systemd-sysusers`: "Protocol driver not attached").
- **When proot can't start programs inside the distro, the install says so** at "Setting up <distro>" ("Linux can't start on this phone") instead of failing later at "Refreshing package lists" with the wrong reason, and doesn't retry what can't work. On a 32-bit phone with Termux from the Play Store, whose proot can't run any distro, it says to install Termux from F-Droid (backing up first); elsewhere it asks for a report. Other proot errors are no longer reported as a bad proot option.
- **No sound is said once, plainly.** When Termux's PulseAudio doesn't start, starting a distro said "andronix: sound isn't set up (exit status 1)" every time. It now says "No sound: PulseAudio didn't start in Termux. Everything else works" once, and again only if sound worked in between; the details stay in the Termux log.
- **The password questions say what typing looks like**: each character shows as *, or, in the plain (ASCII) prompts, nothing shows, "and that's normal". The second question is "Type the same password again" instead of "Type it again", and if the two don't match it says "The two passwords didn't match. Type both again." and asks for both, instead of only the second.
- **The Username question says what it takes**: only the name (the password is asked next), lower-case letters, digits, - or _, starting with a letter, with an example. A refused name says why: a space, a capital letter (with the lower-case name to use), a character that isn't allowed, a digit first, or too long.
- **An install ends with how to start it again**: "Start Kali Linux any time, from Termux: andronix start kali" after a command-line install, or the `andronix desktop` command after a desktop install. The summary shows `andronix start <distro>`, which works from any folder (the `./start-<distro>.sh` scripts still work).
- **"Your older <distro> is untouched" shows once**, on the install that kept the old start script, not on every later install of that distro.

### Telemetry

- **A failed download says why** (HTTP status, DNS, connection, timeout, interrupted, checksum, disk full), without the address or any path.
- **Only released versions send telemetry**: test, release-candidate and development builds send nothing, so test runs never mix into the numbers. `andronix telemetry off` works as before.

### Reports

- **`andronix report --attach AX-…` joins a report to the one you sent from the Andronix app**, so support sees both together. `--message` works as before.

## 2.0.6 (2026-10-07)

### Installs that get through

- **The Andronix server moves to api.andronix.app.** get.sh and the installer (finding the download, the beta channel, Modded downloads, reports, telemetry) use api.andronix.app first and fall back to the old products.andronix.xyz only when it can't be reached.

- **A package-signing key problem is named as such.** On some phones Arch's key tool (gpg-agent) won't start under proot, so the first package refresh can't make the keyring. The installer now clears the half-made keyring and tries once more, and if it still fails it says so (instead of "Couldn't reach the package servers") and asks for a report. Other non-network failures while refreshing package lists no longer say the servers were unreachable.

### Termux:X11

- **A missing Termux:X11 app is named at the end of the install.** When a desktop install finishes and the Termux:X11 app isn't installed, a box says where to get it (termux-x11-universal-debug.apk from the nightly release) and links the docs page with pictures. `andronix desktop` shows the same box. When Android won't say whether the app is installed and Termux:X11 then doesn't start, the fix names the app first.

### Telemetry

- With telemetry on, a failed install or update caused by the distro's package manager also reports proot's version and whether Termux started andronix through Android's linker (Play Store Termux), to find why package lists fail on some Play Store Termux phones with older kernels.

## 2.0.5 (2026-10-06)

### Phones with older kernels

- **The desktop isn't ruled out any more.** On phones whose Linux kernel is older than 4.8, the warning now says the desktop may stay black, and what to try then (`andronix desktop --legacy-drawing`, or VNC with `vncserver-start`), instead of suggesting a command-line-only install. Desktops work on many such phones.

### Installs that get through

- **A failing apt hook no longer stops the install.** Under proot, some optional apt hooks fail (command-not-found's database, appstream). The installer turns off just the hook that failed and goes on, and `/etc/andronix/apt-hooks-disabled.txt` says how to turn it back on.
- **A Wi-Fi sign-in page or filter is named as such.** When downloads arrive changed ("InRelease is not signed", "NOSPLIT") on every mirror, the message says to sign in to the Wi-Fi or switch networks, instead of blaming the mirrors.
- **When proot refuses an option**, the installer tries again with only the basic ones, and suggests reinstalling proot if that fails too.
- `andronix pack debian …` typed inside a distro drops the distro name: packs go into the distro you're in.
- **Stopping the user setup with Ctrl+C no longer leaves you as root.** During the install it now says the distro is installed but your user isn't set up, and doesn't open a root shell. On the first start, Ctrl+C at the same questions leaves the distro instead of staying as root. Either way, the next start asks again.
- **`andronix` works under any name or path on Play Store Termux.** A copy run as `./andronix-new` or from another folder no longer reads its own path as the command ("Unknown command '/data/…'").
- **Termux commands typed inside a distro say where to type them.** `andronix install`, `start`, `remove`, `update`, `list`, `backup`, `restore`, `clean`, `tune`, `display` and `doctor` typed inside a distro now show the same two steps as `andronix desktop` (type exit, then run it in Termux), instead of failing with "proot is missing".

### Black screens

- **LXQt shows its wallpaper again on phones under 3 GB of RAM.** The light profile's settings hid LXQt's desktop defaults, so the desktop came up black (panel only) since 2.0.0. `andronix update` repairs existing installs, including desktops that saved the black settings. On Debian, LXQt's panel was black too: the light profile hid LXQt's own default settings (theme and panel layout), and now keeps them.

### Settings survive a sudden stop

- Android can stop Termux at any moment. The installer's small settings files (the install's state, the performance profile, the distro's release info, telemetry's choice) are now written so that they're either the old version or the new one, never empty. An empty profile file left by an older version counts as unset.

### Faster desktops

- **XFCE's first start is faster.** A new home's first desktop start no longer waits while the window manager saves about 80 default settings one at a time. The installer and `andronix update` write those defaults once, system-wide, when the distro has none. Nothing looks different, and your own settings still win.

### Telemetry

- With telemetry on, `andronix desktop` reports once, 20 seconds in, whether the Termux:X11 screen shows anything (a yes or no, plus the kernel version), so decisions about older kernels can rest on data. A few rows of the screen are read into memory to tell; no picture is kept or sent.

## 2.0.4 (2026-10-06)

### Installs that get through

- **Termux's own packages are repaired like the distro's.** When installing proot or Termux:X11 fails, the installer finishes an interrupted dpkg, waits for a locked package manager, fetches fresh package lists, and for network errors pauses and moves to the next verified Termux mirror. The message says which kind of problem it was.
- **The space check asks for what an install really needs.** The numbers come from measuring each distro with XFCE, Firefox and video codecs. The old ones were 40–70% too high: Debian XFCE asked for 2070 MB and uses 1213. A resumed install needs no space for the image, and the message says how much to free.
- **`andronix: command not found` after installing explains itself.** get.sh stops with a clear message when installing the `andronix` binary fails or it doesn't start, finds Termux's `bin` folder even without `$PREFIX`, and says when that folder isn't on `PATH`.

### Black screens

- **`--legacy-drawing` and `--force-bgra` are remembered.** If one of them fixed a black Termux:X11 screen, `andronix desktop` uses it on every start. `--x11-default` forgets them. `andronix doctor` shows the saved options and the Termux:X11 versions.
- **Typing `andronix desktop` inside a distro** says where to type it instead of failing.

### Telemetry

- With telemetry on, events also say where the installer runs (Termux, a distro, Linux) and the Termux version, so problems can be told apart by setup. Nothing personal is added.

## 2.0.3 (2026-10-05)

### Installs that get through

- **Package downloads retry and switch mirrors.** When the package servers can't be reached, the installer waits and tries again, then moves to another mirror: Ubuntu, Debian, Kali and Void each have verified fallbacks. A refresh where only some sources fail goes on with the ones that answered.
- **Common package-manager problems are repaired, not reported.** An interrupted dpkg is finished, corrupt or stale package lists are fetched again, broken dependencies are fixed, and a phone with a wrong clock no longer fails apt's date checks.
- **Clearer messages when it still fails.** The error now says what went wrong (the server didn't answer, the phone ran out of space, Android stopped the install) and what to do about it.

### Commands you type

- **Small typos are forgiven.** A distro or desktop name with a typo, or cut short, is used with a note: `andronix install debia --de xfc` installs Debian with XFCE. Commands that delete something (like `remove`) only suggest the name, they never act on a guess.
- **`plasma` means KDE Plasma.** GNOME, Cinnamon and other desktops Andronix doesn't offer now say so, with the list of desktops you can choose.

### Already installed

- **Installing what's already there isn't an error any more.** If another edition of the same distro is installed (the free one, and you run a Modded command), the installer asks whether to keep it or replace it; from the app it explains both ways and ends normally. The Andronix app is told, so it can offer to reinstall.
- **A reinstall never deletes your distro for nothing.** A Modded reinstall whose download link has expired stops before deleting anything.

## 2.0.2 (2026-10-04)

### Downloads

- **Downloads work on phones whose DNS setup confused the installer.** On some phones (seen on a moto g57 power with Android 16), Termux's `resolv.conf` lists `127.0.0.1` first, and nothing answers there. The installer kept asking that address and failed with "lookup … connection refused" at "Finding the best download". It now asks the real nameservers first (the phone's, then Termux's, then 8.8.8.8 and 1.1.1.1), tries loopback addresses last, and moves on when a server refuses or doesn't answer. This also covers an `/etc/resolv.conf` that is empty or lists only loopback addresses.

### Install

- **The app's command pasted twice now runs once.** Pasting the command a second time before pressing Enter glued the two copies together (`--de xfceexport ANDRONIX_INSTALL_ID=…`), and the install stopped with an unknown desktop. The installer now keeps the first command, drops the second copy, and says "It looks like the command was pasted twice; running it once."
- **The Andronix app hears about the install result more reliably:** the installer also tries the `am` found on `PATH`.

### Reporting a problem

- **`andronix report`** sends a problem report to Andronix support. It records the last failed command (which step, the error and the log). Before sending, it shows exactly what it will send, with purchase tokens, signed links, email addresses and user names removed, and asks for one line about what went wrong. It then opens the report in the Andronix app, or prints a link and an ID when the app isn't there. `andronix report --help` lists what's sent; `--dry-run` only shows it.

### Smaller fixes

- Text wraps cleanly on narrow phone screens: boxes and lists wrap under their own indent, and only at spaces, so commands like `./start-debian.sh` are never split.
- `andronix update` re-runs a Modded XFCE image's panel setup after updating packages, so its panel plugins keep working.

## 2.0.1 (2026-09-27)

### Works on more phones

- **Phone compatibility, automatic.** The installer measures what the phone's kernel and Termux can do (inside the distro, under proot) and applies the fixes that phone needs, from rules shipped in the installer. `andronix doctor` shows the results and the fixes that apply. No personal data is read; with telemetry on, only the result summary is sent.
- **Kali (and other systemd 256+ distros) on kernels before 5.8.** systemd couldn't read its own config under proot there ("Protocol driver not attached"), which left sudo, dbus and the desktop unconfigured (seen on a Redmi Note 7 Pro, kernel 4.14). A small shim now gives it what it needs, and Kali uses the standalone tmpfiles and sysusers.
- **Resuming an install works across installer versions.** An install that stopped part way (also one started by 2.0.0) gets this phone's fixes and finishes its desktop when you run `andronix install <distro>` again. `andronix update` on an unfinished install now says to run `andronix install`.
- **Kernels before 4.8 (many Android 8 and 9 phones):** the command line works. Desktops stay black there, and the installer now says so at install and before starting a desktop, and suggests `--de none`.
- **LineageOS** is recognised in `andronix doctor`.

### Desktop

- **Termux:X11 first.** After an install, and when you log in to a distro, the installer suggests `andronix desktop <distro>` to open the desktop in the Termux:X11 app. VNC is shown second.
- **Termux:X11's extra-keys bar** is hidden on the first desktop, so the desktop's bottom panel shows. Swipe down with three fingers to bring it back.
- **Fewer processes, so Android 12+ stops the desktop less often** (the phantom process killer counts an app's processes): Firefox runs in fewer processes and without its crash reporter, the distro doesn't start its own PulseAudio, and autostart programs that don't work on a phone are off. On Android 12 and newer, `andronix desktop` prints one line with how to prevent "signal 9".
- **Firefox plays H.264 video** (YouTube falls back to it on phones): video codecs are installed with Firefox on Debian, Kali, Ubuntu and Fedora.
- **XFCE's Web Browser button opens Firefox** on Debian and Kali (it asked for a "preferred application" before).
- **Wallpapers fit portrait phones** on LXQt and KDE Plasma, without cropping the logo.

### Downloads and storage

- **Downloads resolve through the Andronix API first.** If the API is slow or down, the installer falls back to dl.andronix.app after 3 seconds. Every download is still checked against its sha256.
- **Modded downloads are checked too:** the installer fetches the edition's sha256 from the Andronix API, behind the same purchase token, and verifies the download.
- **Less storage used.** A finished install deletes its download, Modded and Classic images included. `andronix remove` also deletes the distro's leftover downloads, and the new `andronix clean` empties the download cache.
- **get.sh retries a download that was cut off** mid-transfer on a bad connection.
- **Removing a Modded or Classic edition** (the app's uninstall): `andronix remove <edition-id> --legacy` removes only that edition, never the free distro or another edition in the same folder. With a Classic id, `--legacy` also removes the copy the old app's Modded scripts installed. The confirmation shows each folder's size.

### Premium

- **Beta channel:** `andronix update --channel beta --token '<token from the app>'`. `andronix update --channel stable` goes back to the release.
- **Early-access Modded editions.** Premium and Modded Pass owners can install them before everyone else. Anyone else sees "Early access: Premium" instead of a download error.
- A Modded token for a different edition now says so, instead of "link expired".

### Smaller fixes

- Output fits the terminal's width (a stale `COLUMNS` made it wrap at 56 columns).
- Logs rotate per kind, and the log of the running command is never deleted.
- The unused TigerVNC wrapper path was removed; the VNC desktop starts the same way on every distro.

## 2.0.0 (2026-09-26)

Andronix 2.0: a new installer, written in Go, published at dl.andronix.app.

### Install

- **One command:**

  ```sh
  curl -fsSL https://dl.andronix.app/get.sh -o $PREFIX/tmp/get.sh && sh $PREFIX/tmp/get.sh install <distro> --de <de>
  ```

- **Termux:** it works with Termux from F-Droid, GitHub and Google Play. Play Termux needs Android 11 or newer.
- **proot and a half-upgraded Termux:** the installer installs proot itself if it's missing. It also repairs a half-upgraded Termux (where curl can't start) with one package upgrade that keeps your edited settings.
- **Every download is checksummed.**

### Distros and desktops

- **9 free distros:** Ubuntu 26.04 LTS, Ubuntu 24.04 LTS, Debian 13, Kali rolling, Fedora 44, Arch Linux ARM, Manjaro ARM, Alpine 3.24 and Void.
- **Desktops:** XFCE, LXQt, MATE and KDE Plasma. KDE is offered on Ubuntu, Debian and Kali, and needs 4 GB of RAM.
- **Firefox** is included.
- **A normal user account** is created, so you no longer run everything as root.
- **Window managers are retired.** Old window-manager installs now install XFCE.

### Desktop and commands

- **Termux:X11:** `andronix desktop <distro>` opens the desktop in the Termux:X11 app. It's smoother than VNC, and sound works out of the box because PulseAudio is set up automatically. Termux:X11 comes from GitHub (termux-x11 nightly, the universal-debug APK).
- **VNC** still works: `vncserver-start` inside a distro.
- **New commands:**
  - `andronix update`: updates the installer and your distros.
  - `andronix backup --to storage`: backs up to phone storage.
  - `andronix tune`: performance profile.
  - Dev packs: `andronix pack add python|node|java|go|db`. PostgreSQL works under proot.

### Modded OS

- **Modded 2.0 editions**, installed with `--edition <id>`: Ubuntu 26.04 XFCE, Ubuntu 26.04 KDE (Plasma 6), Debian 13 XFCE, Manjaro XFCE and Kali XFCE. They share one Andronix look, and updates are included.
- **Classic editions:** the original Modded Ubuntu XFCE, Ubuntu KDE, Debian and Manjaro. They keep the original look, rebuilt on current systems. The old Modded install commands install the matching Classic edition.

### Fixes

- **Android 8 to 11:** the installer no longer crashes (SIGSYS).
- **Android 9 on kernel 4.4:** proot no longer aborts.
- **Ubuntu 26.04 on Play Store Termux:** updates no longer break the system. It switches to GNU coreutils first.

### Privacy

- **Optional, anonymous install events:** distro, desktop, result. There are no names and no emails.
- **Turn them off** with `andronix telemetry off` or `ANDRONIX_NO_TELEMETRY=1`.

### Known issue

- On some Android 9 phones with a 4.4 kernel, desktops may show a black screen. The command line works, and the installer warns you before a desktop install.
