#!/usr/bin/env bash
# Build ready-made desktop images (internal/app/image.go). Never uploads.
#
#   ci/build-image.sh debian xfce        one image
#   ci/build-image.sh --all              every line of ci/images.conf
#
# For each image, in the Termux-like test box (tests/Dockerfile: arm64 Linux,
# non-root, Termux's proot), exactly what a phone does for a free install:
#   1. the base rootfs: our published tarball (checked against its .sha256),
#      or a base:<distro> override from ci/images.conf (Kali: OffSec's);
#   2. andronix install <distro> --de <desktop> --image-build: the phone's
#      package steps (refresh, update, desktop packages and the optional
#      ones) plus Xvfb and PulseAudio, and nothing else; no browser;
#   3. ci/clean-rootfs.sh, ci/scrub-image.sh, IMAGE=1 ci/check-image.sh;
#   4. a reproducible tar.zst: sorted names, owners 0, mtimes clamped to
#      SOURCE_DATE_EPOCH, no atime/ctime; plus .sha256, .meta and .manifest
#      (binary and source package versions).
# Output, dated by SOURCE_DATE_EPOCH (images are never overwritten once published):
#   dist/images/<distro>-<ver>-<de>-<arch>-<YYYYMMDD>.tar.zst (+ .sha256, .meta, .manifest)
#   dist/images/<distro>-<ver>-<de>-<arch>.json   the pointer the installer reads
#                                                 (published last: ci/publish-r2.sh image)
#
# Env: ZSTD_LEVEL (default 19), SOURCE_DATE_EPOCH (default: today 00:00 UTC),
# KEEP=1 keeps the build box, ANDRONIX_SRC as in mods/build-modded.sh.
set -u
cd "$(dirname "$0")/.." || exit 1
repo=$PWD
src=$(cd "${ANDRONIX_SRC:-$repo}" && pwd)
arch=aarch64
level=${ZSTD_LEVEL:-19}
: "${SOURCE_DATE_EPOCH:=$(( $(date -u +%s) / 86400 * 86400 ))}"
export SOURCE_DATE_EPOCH
[ -x "$src/dist/andronix-linux-$arch" ] || { echo "no $src/dist/andronix-linux-$arch; run ci/build-go.sh there"; exit 1; }
mkdir -p dist/images cache/images
if command -v sha256sum >/dev/null; then sum=(sha256sum); else sum=(shasum -a 256); fi

conf_override() { # base URL override for a distro, from ci/images.conf (OUR_BASE=1: ignore)
    [ -n "${OUR_BASE:-}" ] && return 0
    awk -v d="base:$1" '$1 == d { print $2 }' ci/images.conf | head -1
}

# base <distro> <version> -> path of a checked base tarball in cache/images
base() {
    local d=$1 v=$2 url name f want
    url=$(conf_override "$d")
    if [ -z "$url" ]; then
        name="$d-$v-$arch.tar.xz"
        url="${ANDRONIX_MIRROR:-https://dl.andronix.app}/rootfs/$d/$v/$name"
    fi
    name=$(basename "$url")
    f="cache/images/$name"
    want=$(curl -fsSL "$url.sha256" 2>/dev/null | awk '{ print $1; exit }')
    if [ -z "$want" ]; then # OffSec style: SHA256SUMS next to it
        want=$(curl -fsSL "$(dirname "$url")/SHA256SUMS" 2>/dev/null | awk -v n="$name" '$2 == n || $2 == "*" n { print $1; exit }')
    fi
    [ -n "$want" ] || { echo "no checksum for $url" >&2; return 1; }
    if [ ! -f "$f" ] || [ "$("${sum[@]}" "$f" | awk '{ print $1 }')" != "$want" ]; then
        curl -fL --retry 3 -o "$f.part" "$url" >&2 || return 1
        [ "$("${sum[@]}" "$f.part" | awk '{ print $1 }')" = "$want" ] || { echo "base $url: checksum mismatch" >&2; rm -f "$f.part"; return 1; }
        mv -f "$f.part" "$f"
    fi
    # A base packed under one top folder (OffSec's kali-arm64/) is re-rooted
    # once, so the installer finds etc/passwd at the top (ANDRONIX_ROOTFS).
    # One top-level folder and nothing else (OffSec's kali-arm64/): re-root.
    top=$(xz -dc "$f" 2>/dev/null | tar -tf - 2>/dev/null | sed 's|^\./||; s|/.*||' | grep -v '^$' | sort -u)
    case "$(printf '%s\n' "$top" | wc -l | tr -d ' ')" in
        1)
            top="$top/"
            local rr="cache/images/${name%.tar.xz}-rerooted-${want:0:12}.tar.xz"
            if [ ! -f "$rr" ]; then
                echo "    re-rooting $name (it unpacks into $top)" >&2
                docker run -i --rm -v "$repo/cache/images":/c debian:13 bash -c "
                    { apt-get update -qq && apt-get install -y -qq xz-utils; } >/dev/null 2>&1
                    mkdir -p /r && tar -xJf /c/$name -C /r --strip-components=1 --numeric-owner &&
                    tar -C /r --numeric-owner -cf - . | xz -T0 -6 >/c/$(basename "$rr").part" &&
                    mv -f "$rr.part" "$rr" || { echo "re-rooting $name failed" >&2; return 1; }
            fi
            f=$rr
            ;;
    esac
    printf '%s  %s\n' "$("${sum[@]}" "$f" | awk '{ print $1 }')" "$(basename "$f")" >"$f.sha256"
    printf '%s %s %s\n' "$f" "$want" "$url"
}

build() {
    local distro=$1 de=$2 version out name box R A t0 base_f base_sum base_url rc
    version=$(sed -n 's/^DISTRO_VERSION="\{0,1\}\([^"]*\)"\{0,1\}/\1/p' "$src/distros/$distro.conf")
    base_name="$distro-$version-$de-$arch"
    stamp=$(date -u -r "$SOURCE_DATE_EPOCH" +%Y%m%d 2>/dev/null || date -u -d "@$SOURCE_DATE_EPOCH" +%Y%m%d)
    name="$base_name-$stamp"
    out="dist/images/$name"
    log="cache/images/$name.log"
    : >"$log"
    t0=$(date +%s)
    echo "==> $name"
    read -r base_f base_sum base_url < <(base "$distro" "$version") || { echo "    FAIL: no base rootfs"; return 1; }
    box="image-build-$name-$$"
    docker build -q -t andronix-test -f tests/Dockerfile tests >/dev/null 2>&1
    docker rm -f "$box" >/dev/null 2>&1
    docker run -d --init --name "$box" -v "$src":/src:ro -v "$repo/cache/images":/base:ro \
        -e SOURCE_DATE_EPOCH andronix-test sleep infinity >/dev/null
    [ -n "${KEEP:-}" ] || trap 'docker rm -f "$box" >/dev/null 2>&1' RETURN
    A=/home/termux/.local/bin/andronix
    R=/home/termux/.andronix/distros/$distro/rootfs
    # 2. The phone's package steps, from our base.
    docker exec -e ANDRONIX_NO_TELEMETRY=1 "$box" bash -c "ANDRONIX_SRC=/src sh /src/get.sh >/dev/null && \
        ANDRONIX_ROOTFS=/base/$(basename "$base_f") $A install $distro --de $de --yes --plain --image-build" >>"$log" 2>&1
    rc=$?
    [ "$rc" = 0 ] || { echo "    FAIL: install --image-build exited $rc (see $log)"; tail -5 "$log"; return 1; }
    # The manifest (binary and source package versions), written by --image-build.
    docker exec "$box" cat "/home/termux/.andronix/distros/$distro/image-manifest.txt" >"$out.manifest" 2>/dev/null || : >"$out.manifest"
    # 3. Clean, scrub, gate (as root: some files are read-only).
    docker exec -u root "$box" bash -c "bash /src/ci/clean-rootfs.sh $R && bash /src/ci/scrub-image.sh $R" >>"$log" 2>&1 ||
        { echo "    FAIL: clean/scrub (see $log)"; return 1; }
    if ! docker exec -u root -e IMAGE=1 "$box" bash /src/ci/check-image.sh "$R" >"$out.hygiene" 2>&1; then
        echo "    FAIL: hygiene"; grep FAIL "$out.hygiene" | head -5; return 1
    fi
    bad=$(docker exec -u root "$box" sh -c "find $R -xdev -type l -lname '/home/termux/*' | head -3")
    [ -z "$bad" ] || { echo "    FAIL: symlinks into the build box: $bad"; return 1; }
    unpacked=$(docker exec -u root "$box" sh -c "du -sb --apparent-size $R | cut -f1")
    files=$(docker exec -u root "$box" sh -c "find $R -xdev | wc -l")
    # 4. Reproducible tar, zstd in a helper container (the box has no zstd).
    docker exec -u root "$box" sh -c "cd $R && tar --sort=name --format=posix \
            --pax-option=exthdr.name=%d/PaxHeaders/%f,delete=atime,delete=ctime \
            --mtime=@$SOURCE_DATE_EPOCH --clamp-mtime --numeric-owner --owner=0 --group=0 \
            -cf - ." |
        docker run -i --rm debian:13 bash -c 'command -v zstd >/dev/null || { apt-get update -qq && apt-get install -y -qq zstd; } >/dev/null 2>&1
            zstd -q -T0 -'"$level"' -c' >"$out.tar.zst.part" ||
        { echo "    FAIL: export"; rm -f "$out.tar.zst.part"; return 1; }
    mv -f "$out.tar.zst.part" "$out.tar.zst"
    sha=$("${sum[@]}" "$out.tar.zst" | awk '{ print $1 }')
    printf '%s  %s\n' "$sha" "$name.tar.zst" >"$out.tar.zst.sha256"
    printf 'size=%s\nunpacked=%s\nfiles=%s\npackages=%s\nbase=%s\nbase_sha256=%s\ninstaller=%s\nzstd=%s\nsource_date_epoch=%s\nbuilt=%s\nbuild_seconds=%s\n' \
        "$(wc -c <"$out.tar.zst" | tr -d ' ')" "$unpacked" "$files" "$(wc -l <"$out.manifest" | tr -d ' ')" \
        "$base_url" "$base_sum" "$(docker exec "$box" $A version 2>/dev/null | head -1)" "$level" \
        "$SOURCE_DATE_EPOCH" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$(( $(date +%s) - t0 ))" >"$out.meta"
    # The pointer: which dated image is current, with its checksum and sizes.
    printf '{"url":"rootfs/%s/%s/%s.tar.zst","sha256":"%s","size":%s,"unpacked":%s,"built":"%s"}\n' \
        "$distro" "$version" "$name" "$sha" "$(wc -c <"$out.tar.zst" | tr -d ' ')" "$unpacked" "$stamp" >"dist/images/$base_name.json"
    echo "    $(du -h "$out.tar.zst" | cut -f1) ($unpacked bytes unpacked, $files files) in $(( $(date +%s) - t0 )) s"
    echo "    $sha"
}

if [ "${1:-}" = --all ]; then
    status=0
    # fd 3: the docker runs inside build() read stdin.
    while read -r d de _ <&3; do
        case "$d" in ''|'#'*|base:*) continue ;; esac
        build "$d" "$de" </dev/null || status=1
    done 3<ci/images.conf
    exit $status
fi
build "${1:?usage: build-image.sh <distro> <desktop> | --all}" "${2:?usage: build-image.sh <distro> <desktop>}"
