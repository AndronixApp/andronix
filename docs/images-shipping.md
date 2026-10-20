# Ready-made images: shipping plan

Status: proposal for the lead, 2026-10-06. Nothing is published. Measured results: [images.md](images.md). Licences and what the owner deferred: [images-step0.md](images-step0.md).

## 1. Where they live

- **Bucket:** the public `andronix-dl` bucket (`dl.andronix.app`), next to the base tarballs. Free images only; never anything Modded.
  - `rootfs/<id>/<ver>/<id>-<ver>-<de>-aarch64-<YYYYMMDD>.tar.zst`: dated, never overwritten.
  - `rootfs/<id>/<ver>/<id>-<ver>-<de>-aarch64.json`: the pointer, one small object: `{url, sha256, size, unpacked, built}`.
- **Why a pointer:**
  - The image is replaced by writing one object, so no client can read a new image with an old checksum.
  - Deleting the pointer is the kill switch: installers fall back to the package install (`no_image`).
- **Installer change needed (small, in image.go):** read the `.json` pointer first, falling back to today's fixed name, which stays for local tests. The lead approved the layout on 2026-10-06; not written yet.
- **ci/publish-r2.sh:** its allow-list needs one more pattern for `-<de>-aarch64-<date>.tar.zst` and `.json`. The Modded/paid refusals stay.
- **Size:**
  - XFCE ×9: 2.3 GB; KDE ×3 (ubuntu, debian, kali): 1.35 GB. 3.6 GB per generation.
  - Keeping the current and the previous generation, so a bad build can be rolled back: about 7.3 GB.
  - Per install: 160-480 MB, against about 230-300 MB of apt downloads today. Similar, so the time saved is dpkg work on the phone, not download.
- **Cost** ([R2 pricing](https://developers.cloudflare.com/r2/pricing/), checked 2026-10-06):
  - Standard storage is $0.015 per GB-month, and egress is free. So 7.3 GB costs at most about **$0.11/month**. The free tier's 10 GB-month is shared with the base tarballs already in the bucket.
  - Reads (Class B) cost $0.36 per million, which is nothing at our volume.

## 2. How the installer picks (code on feat-images, not merged yet)

- **Branch:** feat-images has 20 commits on top of next-2.1. distros asked for these gates, and all have passed:
  - unit tests;
  - the standalone matrix with images (9/9);
  - the Termux matrix with images (9/9) and one without (debian).
- **Rule:**
  - A free install with a desktop, and an image published for that distro, version, desktop and arch, uses the image.
  - Everything else takes today's path: editions, Modded, Classic, `--de none`, `ANDRONIX_ROOTFS`.
- **Silent fallback** to the package install when the image is missing, unreachable, fails its checksum, fails to download or unpack, or lacks the desktop. The reason goes to install.conf `FALLBACK` and to telemetry `fallback_reason` (allow-listed in products-api 1576a6c).
- **After unpacking,** everything runs as today: setup, compat, refresh, update (the "quick update"), browser (Firefox is never baked in), desktop-setup, user.
- **Existing installs** are never touched.

## 3. Rebuilds, and the day-old update cost

- **Measured:** Debian on a same-day image needed 0 updates (3 s). Arch on a 1-day-old image needed 19 packages (93 s). Rolling distros age fastest.
- **Proposed cadence:**
  - Daily: arch, manjaro, kali, void (rolling) and fedora (fast-moving updates).
  - Weekly: debian, ubuntu ×2, alpine.
  - About 6 min per image on an arm64 builder: roughly 35 min a day, plus about 30 min a week for the stable ones and KDE.
- **Where:** a GitHub Actions arm64 runner (the base rootfs workflow already exists), with a scheduled job:
  1. build;
  2. hygiene gate;
  3. publish the dated image;
  4. smoke-install it in the test box;
  5. only then update the `.json` pointer;
  6. delete images older than the previous generation.
- **CI minutes and cost** (estimates, owner's call):
  - **The repo:** AndronixApp/andronix-installer-dev is **private**, so minutes are billed. Its rootfs workflow already runs on `ubuntu-24.04-arm`.
  - **Pricing:** arm64 standard runners are available to private repos since 2026-01-29 ([changelog](https://github.blog/changelog/2026-01-29-arm64-standard-runners-are-now-available-in-private-repositories/)).
    - Linux 2-core arm64 is **$0.005/min**, rounded up per job ([pricing](https://docs.github.com/en/billing/reference/actions-runner-pricing)).
    - It counts against the plan's included minutes first. How many the plan includes isn't on that page: check the org's billing settings. Other workflows share them.
  - **Runner:** 2 cores, 8 GB RAM, 14 GB SSD. Enough for the largest image (Arch: about 1.8 GB unpacked).
  - **Per-image minutes:** the Mac build time (M-series, many cores) × 2.5 for 2 cores, plus about 3 min of setup (Go build, the test box, the base download). These are unmeasured guesses, ±50%; one CI run would settle them.

    | Image | Mac build | CI estimate |
    |---|---|---|
    | alpine xfce | 136 s | ~9 min |
    | void xfce | 202 s | ~11 min |
    | ubuntu 26.04 xfce | 212 s | ~12 min |
    | ubuntu 24.04 xfce | 218 s | ~12 min |
    | kali xfce | 243 s | ~13 min |
    | debian xfce | 263 s | ~14 min |
    | fedora xfce | 311 s | ~16 min |
    | manjaro xfce | 406 s | ~20 min |
    | arch xfce | 412 s | ~20 min |
    | debian kde | 355 s | ~18 min |
    | kali kde | 360 s | ~18 min |
    | ubuntu 26.04 kde | 435 s | ~21 min |
    | **All 12** | | **~184 min** |

  - **Measured, first CI run** (PR #1, run 37501364749, 2026-10-06; build + hygiene gate + smoke install, no publish; all 12 passed):

    | Image | Minutes |
    |---|---|
    | alpine xfce | 4.4 |
    | debian xfce | 5.7 |
    | fedora xfce | 5.9 |
    | ubuntu24 xfce | 6.0 |
    | ubuntu xfce | 6.1 |
    | kali xfce | 6.5 |
    | void xfce | 6.5 |
    | arch xfce | 10.5 |
    | ubuntu kde | 10.6 |
    | manjaro xfce | 11.3 |
    | kali kde | 11.7 |
    | debian kde | 12.1 |
    | plan job | 0.1 |

    - **Total:** 97 min, **105 billed minutes** (rounded up per job). Wall clock 29 min, 4 in parallel.
    - **Weekly (the owner's choice):** about 455 min/month, **about $2.30/month** if all of it is billed at $0.005/min. That's well below the 184-minute estimate: the runners' Docker and zstd are faster than I assumed.
  - **Schedules:**
    - **A. Everything weekly:** ~184 min/week, ~800 min/month, **~$4/month** if all of it is billed. Rolling distros' first-start update then grows to a week of packages (Arch: 19 packages after one day, so expect a few minutes after a week).
    - **B. Rolling daily, the rest weekly:**
      - Daily: arch, manjaro, kali xfce+kde, void, fedora, ~98 min/day, about 2,950 min/month.
      - Weekly: debian ×2, ubuntu ×3, alpine, ~86 min/week, about 370 min/month.
      - Total about 3,300 min/month, **~$17/month** if all billed.
    - **C. Daily for everything:** ~5,500 min/month, **~$28/month**.
  - **Free alternative:** a self-hosted runner on our own arm64 Mac costs nothing, but competes with the emulators for host load. Not recommended while the Mac is shared.
- **Why not skip the update step for young images:** the owner asked for an update on first start. The update stays; daily builds keep it to seconds or tens of seconds.
- **Reproducibility:** same-day builds are byte-identical. Pinning apt to a snapshot date (snapshot.debian.org / snapshot.ubuntu.com) is the remaining step, and only matters for audits.

## 4. Which release carries it

- **Installer 2.1 (next-2.1):** merge feat-images (distros reviews and merges), with the pointer change from section 1.
- **Termux users:** get it with the 2.1 installer, via self-update or get.sh. No app release needed.
- **Standalone users:** need an app build with 2.1 in jniLibs. Beta8 is the first candidate if 2.1 is ready by then, otherwise the beta after 2.1.
- **Server side first:** images on R2 can be uploaded before any client knows about them. Clients only use an image once its pointer exists, so the publish order is: images, smoke test, pointers.

## 5. Before publishing (lead/owner)

- Your OK to upload to R2. Nothing is uploaded yet.
- Still deferred by the owner: the source mirror (GPL) and the questions to Canonical and Debian (see images-step0.md).
- Store copy, if any: "based on Debian/Ubuntu" wording, plus the not-affiliated note.

## Open questions

1. ~~Pointer layout~~: approved by the lead, 2026-10-06.
2. Rebuild schedule A, B or C (owner; see the CI minutes above).
3. Beta8 or later for standalone?
