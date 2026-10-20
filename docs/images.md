# Ready-made desktop images

An image caches the package-install step of a free desktop install: the base distro plus the desktop's packages, Xvfb and PulseAudio. Nothing of Andronix's is inside it. Setup, compat fixes, desktop setup, the user step and launchers all run after unpacking, exactly as for a package install, so installer changes never need a new image. Images are only a cache: whenever there is no image, or it fails, the install silently does today's package install.

Licences and naming: [images-step0.md](images-step0.md), including the items the owner deferred. Nothing is published without the lead's OK.

## On the phone (internal/app/image.go)

- **When:** free installs with a desktop only. Not editions, Modded or Classic, not `--de none`, not when `ANDRONIX_ROOTFS` is set.
- **Where:**
  - `<mirror>/rootfs/<id>/<ver>/<id>-<ver>-<de>-<arch>.tar.zst`, next to the base tarballs.
  - Needs `.sha256`; `.meta` gives the unpacked size.
  - `ANDRONIX_IMAGE=<file>` uses a local image (with `<file>.sha256`); `ANDRONIX_NO_IMAGE=1` turns images off.
- **Download:**
  - Streamed straight into the extractor while hashing, so no second copy on disk.
  - A dropped connection resumes by Range (`netx.Stream`).
  - A failed or interrupted unpack wipes the rootfs; the next attempt starts over.
- **Fallback reasons:** `no_image`, `no_checksum`, `unreachable`, `checksum`, `download`, `unpack`, `no_desktop`, `off`. Each is written to install.conf `FALLBACK`, the log, and telemetry `fallback_reason`.
- **After unpacking:**
  - `PREBUILT=image`: only the desktop package step is skipped.
  - Refresh and update still run; that is the "quick package update", at most a week of upgrades with weekly builds.
  - The browser step installs Firefox on the phone; it is never baked in.
  - `prebuilt()` (Modded's skip-everything) is unchanged.
- **Telemetry** (`install_result`): `source` (image|packages), `fallback_reason`, `download_bytes`, `download_ms`, `unpack_ms`. These are on products-api's allow-list.

## Building (ci/build-image.sh)

```
ci/build-go.sh                    # dist/andronix-* (the builder runs this installer)
ci/build-image.sh debian xfce     # one image
ci/build-image.sh --all           # every line of ci/images.conf
```

- **Steps:**
  1. Our published base tarball, checked against its `.sha256`.
  2. In the Termux-like test box: `andronix install <d> --de <de> --image-build`. This is the phone's own package steps (same package lists and choices), plus Xvfb and PulseAudio (`DISTRO_XVFB`/`DISTRO_PULSE`), then it stops.
  3. `ci/clean-rootfs.sh`, `ci/scrub-image.sh`, `IMAGE=1 ci/check-image.sh`. Nothing of ours or per phone; caches that differ build to build are dropped.
  4. A deterministic tar (sorted names, owners 0, mtimes clamped to `SOURCE_DATE_EPOCH`, no atime/ctime), compressed with zstd -19.
  5. Writes `.sha256`, `.meta` (sizes, base, installer, build time) and `.manifest` (package versions; binary and source for apt).
- **Adding a distro or desktop:** one line in `ci/images.conf`. A different base, if ever needed: `base:<distro> <url>`.
- **Reproducibility:** two builds on the same day are byte-identical. Builds on different days follow the live archive. Pinning apt to snapshot.debian.org / snapshot.ubuntu.com is still to do.

## Testing

- **Unit tests:** `go test ./internal/app -run 'Image|Prebuilt'`. Covers whether an image is chosen, every fallback reason, Range resume, a damaged download vs a bad image, and `PREBUILT=image`.
- **Emulator:**
  - Serve `dist/images` plus the base tarballs from a local Range-capable server, with `adb reverse tcp:8899 tcp:8899`.
  - Then: `tests/emulator/standalone-matrix.sh -s <serial> --jnilibs <dir> --env 'ANDRONIX_MIRROR=http://127.0.0.1:8899'`. Add `ANDRONIX_NO_IMAGE=1` for the baseline.

## Results (measured 2026-10-06, local emulator only, nothing published)

All runs used emulator 5660 (andronix-a16, Android 16, arm64) and XFCE. Images came from a local mirror, unthrottled; apt used the real internet in both paths. The install time is the installer's own run, to done. Standalone: `tests/emulator/standalone-matrix.sh`. Termux: `tests/emulator/test-matrix.sh --app-context`. Raw logs are under `cache/` and `tests/emulator/results/` (not committed).

| Distro | Image (compressed / unpacked) | Standalone with image | Standalone without | Termux with image |
|---|---|---|---|---|
| alpine 3.24 | 203 / 743 MB | 46 s | 82 s* | 33 s |
| arch | 391 / 1781 MB | 265 s** | 754 s* | 249 s** |
| debian 13 | 215 / 867 MB | 62 s | 490 s | 74 s |
| fedora 44 | 212 / 842 MB | 105 s | 645 s | 116 s |
| kali | 304 / 1170 MB | 85 s | 481 s | 102 s |
| manjaro | 406 / 1752 MB | 201 s | 761 s | 220 s |
| ubuntu 26.04 | 190 / 763 MB | 112 s | 521 s | 114 s |
| ubuntu 24.04 | 162 / 687 MB | 109 s | 650 s | 110 s |
| void | 177 / 735 MB | 39 s | 359 s | 63 s |

- **Passes:**
  - Standalone with images: 9/9, all `SOURCE=image`, and the xfwm4 system defaults were present.
  - Standalone without images: 9/9.
  - Termux with images: 9/9, all `SOURCE=image`.
- **Termux without images:** Debian only, as a baseline. It took 2234 s with host load around 23, against 74 s with the image.
- **\*** alpine and arch without images come from the previous day's run, which had higher host load.
- **\*\*** Arch's image was a day old: the update step installed 19 packages (93 s), and Firefox took another 115 s. Earlier, with a fresh image, Arch took 70 s. With weekly rebuilds, the update step for rolling distros grows with the image's age.
- **Every image install still downloads Firefox** in the browser step: 46-115 s here, depending on the distro.
- **Debian through the app's terminal** (a different harness from the matrix):
  - Image: 50 s unthrottled, 150 s at a 20 Mbit/s cap.
  - Package install: 315 s.
- **First desktop start after an image install:** no apt run. Without an image it spends about 58 s installing Xvfb and PulseAudio (sv2-startup's profile).
- **Fallbacks checked:** mirror unreachable, wrong sha256, and the server killed mid-download (resumed by Range). All were silent.

KDE images (built, hygiene clean, not yet installed on an emulator):

| Image | Compressed / unpacked | Packages |
|---|---|---|
| ubuntu 26.04 kde | 409 / 1488 MB | 916 |
| debian 13 kde | 457 / 1690 MB | 885 |
| kali kde | 484 / 1774 MB | 890 |

## 2.1 gate on device (distros, 2026-10-07, drop jnilibs-2.1.0-dev22)

- Standalone matrix with the live images on dl.andronix.app: 9/9 PASS, every install `SOURCE=image` with `FALLBACK` empty.
- The same with `ANDRONIX_NO_IMAGE=1`: 9/9 PASS, every install `SOURCE=tarball`, `FALLBACK=off`, `PREBUILT=no`. The package path is unchanged.
