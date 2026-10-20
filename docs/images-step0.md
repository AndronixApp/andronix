# Ready-made desktop images: step 0 (licences, trademarks) and build plan

Status: draft for the lead, 2026-10-05. Engineering summary, not legal advice. Nothing has been built or published.

## Step 0: go / no-go per distro

| Distro | Verdict | Main conditions |
|---|---|---|
| Debian | Go, with conditions | Mirror the exact source packages ourselves; "based on Debian" naming with a disclaimer; main only. |
| Ubuntu | Go with conditions, but ask Canonical first | Only "unmodified" Ubuntu may be redistributed under the name. Whether our own package selection counts as unmodified is a grey zone. Mirror the sources. Keep Firefox out of the image. |
| Kali (later) | Go, with conditions | Trademark wording rules; mirror the sources (Kali has no snapshot service); export-control clause. Alternative: download Kali's own official rootfs instead of hosting one. |

### Debian

- **Redistribution:** allowed for main. "There are no restrictions on redistribution of the main Debian distribution." (https://www.debian.org/CD/vendors/info)
- **Commercial use:** fine. "it is not the goal of this policy to limit commercial activity around Debian." (https://www.debian.org/trademark)
- **Naming** (https://www.debian.org/trademark):
  - A picker label such as "Debian 13 · XFCE", plus a note that it is built from official Debian packages, is a non-misleading description and is allowed.
  - "Debian" may not be used as the name of a product. So no "Andronix Debian".
  - Nothing may suggest affiliation with or endorsement by the Debian project.
  - The mark may not be abbreviated, hyphenated or combined with other words, so write "based on Debian", not "Debian-based".
  - The website needs a disclaimer, e.g. "Andronix is not affiliated with Debian. Debian is a registered trademark owned by Software in the Public Interest, Inc."
- **Logo:** may describe our product on a page, but may not be part of our product branding. The swirl as a picker icon is a grey zone. It can be settled by emailing trademark@debian.org ("Trademark Use Request").
- **Source code: we must host it ourselves.**
  - Debian's vendor guidance: "Pointing them only to an FTP server from a third party (i.e. the Debian project) is not sufficient!" (https://www.debian.org/CD/vendors/legal, checked).
  - The reason given: the customer's claim is against the distributor, and the third party may update or remove the package.
  - Many packages are GPLv2-only or LGPL-2.1. Their "equivalent access ... from the same place" option means the sources sit next to the binaries.
  - snapshot.debian.org links are a convenience only, not compliance.
  - Built-Using sources count too (Debian Policy 7.8).
- **Sections:** main only. No contrib, non-free or non-free-firmware; proot needs no firmware.

### Ubuntu

- **The policy** (Canonical IP Rights Policy, 15 July 2015, https://canonical.com/legal/intellectual-property-policy, checked):
  - "You can redistribute Ubuntu in its unmodified form, complete with the installer images and packages provided by Canonical."
  - "Any redistribution of modified versions of Ubuntu must be approved, certified or provided by Canonical if you are going to associate it with the Trademarks. Otherwise you must remove and replace the Trademarks."
  - "You cannot use the Trademarks in software titles. If you are producing software for use with or on Ubuntu you may reference Ubuntu, but must avoid: (i) any implication of endorsement."
- **Grey zone:** our image is Ubuntu Base (provided by Canonical) plus unmodified archive packages installed by apt. That arguably counts as unmodified, but it is not one of Canonical's own images, and the current policy has no remix exception.
- **Settle it:** Canonical's contact form (https://ubuntu.com/legal/terms-and-policies/contact-us; "Distribution of Ubuntu" / "Product built on Ubuntu"). The same request should cover the Ubuntu logo in the picker.
- **Precedent:** proot-distro, UserLAnd and others have hosted Ubuntu rootfs tarballs for years with no public action from Canonical. That shows tolerance, not permission.
- **Source:** snapshot.ubuntu.com only promises "at least 2 years", so mirror the sources ourselves, as for Debian.
- **No Ubuntu Pro/ESM:** no repos or tokens in the image.
- **Firefox:**
  - Ubuntu's firefox and chromium packages are snap stubs, which don't work in proot. Today the installer uses Mozilla's APT repo.
  - If we bake Mozilla's Firefox into an image, Mozilla's Unaltered Software Distribution Policy applies: no tying to a purchase, no personal-data collection in that context, and always ship the latest version.
  - Simplest: keep Firefox out of the Ubuntu image and let the browser step install it on the phone, as today.

### Kali (later)

- **EULA** (https://www.kali.org/docs/policy/eula/EULA.txt): unmodified copies may be distributed under the Kali Linux trademark if OffSec's trademark guidelines are followed. Modified copies must replace the trademarked images.
- **Trademark rules** (https://www.kali.org/docs/policy/trademark/):
  - Never abbreviate the mark or combine it with other words.
  - ™ on first mention, plus "KALI LINUX ™ is a trademark of OffSec."
  - No Kali in domain names.
- **Export control:** the EULA bars providing Kali to Country Group E:1 (Cuba, Iran, North Korea, Sudan, Syria). We would need to block those for Kali images.
- **Source:** Kali has no ongoing snapshot archive, so mirroring the sources is mandatory if we host images.
- **Lower-effort option:** OffSec publishes rootfs tarballs for NetHunter Rootless (proot) at kali.download with SHA256SUMS. The app could download those directly, so we redistribute nothing. Whether the "full" one includes XFCE is unverified.

### Firefox ESR in the Debian image

Allowed unmodified. Mozilla's statement in https://bugs.debian.org/815006 covers Debian's firefox-esr branding for Debian and derivatives that only carry the same kind of patches. We ship Debian's binary as-is.

### What we must publish, per image

- The image tarball, its `.sha256` and its `.meta`.
- **`manifest.txt`:** binary package name, version and arch; source package name and version; build time and apt snapshot time; a convenience link to snapshot.debian.org or Launchpad.
- **`sources/`:** every source package (`.dsc` plus orig and debian tarballs) for every installed package, including Built-Using. Stored on the same host as the image, deduplicated across weekly builds, and kept for as long as an image built from it is offered.
- **Inside the image:** keep `/usr/share/doc/*/copyright` and `/var/lib/dpkg/status`; never strip them when slimming.
- **In the app:** a link on the licences screen and in the download index, plus the trademark disclaimers above.

### Note on today's base rootfs

`dl.andronix.app` already serves Debian and Ubuntu base rootfs tarballs. ci/build-rootfs.sh and ci/publish-r2.sh publish no source packages or manifest with them, so the same source obligation probably applies today. The sources pool for images would cover the base rootfs too.

## Short build plan

### What already exists

- The installer already skips the update, desktop and browser steps for a "prebuilt" rootfs: install.conf `PREBUILT=yes` plus the DE session binary present (internal/app/install.go:797).
- mods/build-modded.sh already builds a desktop image by running a free install in a container, then ci/clean-rootfs.sh and ci/check-image.sh.
- `rootfs.Extractor.ExtractReader` already unpacks from a stream (restore uses it).

### Steps

1. **Image contents.**
   - The rootfs as it is after today's refresh, update, desktop and browser steps. Plus Xvfb and PulseAudio, which standalone installs lazily on first start today.
   - Ubuntu: no Firefox (see above).
   - Cleaned with ci/clean-rootfs.sh, plus apt lists and caches dropped.
   - None of our setup is baked in: `rootfs.Configure`, compat probes, shims, desktop-setup, user setup and launchers all still run after unpacking, as today. Installer changes therefore never need a rebuild.
2. **Builder.**
   - The real installer, run in an arm64 container from our pinned base rootfs (by sha256), with apt pointed at snapshot.debian.org / snapshot.ubuntu.com at a fixed timestamp. A new installer option stops after the package steps; to be agreed with distros.
   - Then a deterministic tar: sorted names, `--mtime=@$SOURCE_DATE_EPOCH`, numeric owners, no atime or ctime.
   - Compressed with zstd, which unpacks far faster than single-threaded xz on phones; final level picked by measurement.
   - Also writes `.sha256`, `.meta` (sizes, base sha, snapshot time) and the manifest and sources.
   - Reproducibility check: build twice, compare sha256.
3. **Installer side (agreed with distros).**
   - find-download looks for an image for distro + DE + arch: resolver item `image`, or `<mirror>/images/...`, or `ANDRONIX_IMAGE` for local tests. Free installs only; Modded keeps its own path.
   - The download is streamed straight into the extractor while hashing, with in-process HTTP Range resume, so there is no second copy on disk.
   - Free-space check uses the unpacked size from `.meta`.
   - On a missing image, a sha mismatch or a failed download: wipe the partial rootfs, silently fall back to today's base rootfs and package install, and record the reason.
   - The refresh and update steps still run (`apt-get update` and `full-upgrade`, at most a week of updates): the "quick package update".
   - Existing installs are never touched; the image is only used for new installs.
4. **Telemetry.** Add `source` (image or packages), `fallback_reason`, `download_bytes` and per-step seconds to `install_result`, to compare with today.
5. **Measure.**
   - Today's Debian and Ubuntu XFCE installs vs image installs on the 5660 emulator, from a local HTTP server (`adb reverse`), at the same bandwidth cap.
   - Targets to confirm once measured: compressed image size about 600 MB or less, and install time at least 3x faster than today.
6. **Weekly rebuild.** A GitHub Actions arm64 runner on a cron job builds, checks and reproduces the image. Publishing stays a manual, lead-approved step; nothing goes to R2 now.
7. **Order.** Debian XFCE first (no open trademark question), then Ubuntu XFCE once Canonical answers or you decide.

### Open questions for the lead and owner

1. Ubuntu: ask Canonical (form above) before we publish an "Ubuntu" image? And the logo?
2. Debian logo as the picker icon: ask trademark@debian.org?
3. Budget for the R2 sources pool: an estimated few GB per distro, deduplicated weekly.
4. "Quick package update on first start": is that the install's update step (my assumption), or a background update at the first desktop start?
5. Kali, later: host our own image, or download OffSec's official rootfs?
6. Today's base rootfs on dl.andronix.app has no source mirror: fix it together with the images?

## Deferred by owner (2026-10-05)

The owner decided to build the images now without the compliance extras. Publishing still needs the lead's OK.

- **Deferred:**
  - **No source mirror.** No `sources/` pool and no manifest of exact source versions on R2 for now. This leaves the GPL/LGPL source obligation open (Debian's vendor guidance: pointing to Debian's servers "is not sufficient"). Today's base rootfs on dl.andronix.app has the same gap. Revisit before or soon after publishing.
  - **No request to Canonical.** No written check on calling our assembled image "Ubuntu", or on the Ubuntu logo. The grey zone in the Ubuntu section above stays open.
  - **No request to Debian** about the logo as the picker icon.
- **Kept (cheap):**
  - "Based on Debian" / "based on Ubuntu" wording, plus the not-affiliated disclaimers, wherever the images are described.
  - Firefox is not baked into either image. The browser step installs it on the phone after unpacking, as today. This sidesteps Mozilla's distribution-policy conditions for Ubuntu, and keeps Debian and Ubuntu the same.
  - main only (Debian); no Ubuntu Pro/ESM; copyright files and dpkg status kept in the image.
