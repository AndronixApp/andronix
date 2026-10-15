#!/usr/bin/env bash
# Assembles the native libraries the Andronix app bundles for standalone
# mode (jniLibs), from a released installer build plus the proot build.
#
#   standalone/jnilibs.sh <installer dir> <out dir>
#
# <installer dir> is the installer release as published: the 12 files of
# `VERSION=x ci/build-go.sh` plus SHA256SUMS (cache/rel-<version>, the same
# files as dl.andronix.app/bin/<version>/). libandronix.so is its
# andronix-android-aarch64, byte for byte, so the app runs exactly the
# installer that was tested and released.
#
# libproot.so, libproot-loader.so and libtalloc.so are rebuilt with
# standalone/proot/build.sh (Docker; the pinned sources in $DL, default
# cache/sa-dl) and must
# match standalone/proot/SHA256SUMS: the build is reproducible, so a
# different result means the sources or the toolchain changed. After a
# deliberate change, run with PROOT_UPDATE=1 and commit the new sums.
#
# <out dir> gets:
#   jniLibs/arm64-v8a/*.so   for the app's src/main/jniLibs (copy as is)
#   licenses/                proot (GPL-2.0), talloc (LGPL-3.0+), libandroid-shmem
#   source/                  the source archives, for the public release
#                            standalone-<version> (GPL source offer, README.md)
#   SHA256SUMS               of everything above, relative paths
#   build.json               versions, commits, cgo and sums, for the app's release notes
#
# Nothing is uploaded or published; android's build copies <out dir>.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
die() { echo "jnilibs: $*" >&2; exit 1; }

[ $# -eq 2 ] || die "usage: standalone/jnilibs.sh <installer dir> <out dir>"
inst=$(cd "$1" && pwd) || die "no installer dir $1"
out=$2
[ ! -e "$out" ] || [ -z "$(ls -A "$out")" ] || die "$out isn't empty"
mkdir -p "$out" && out=$(cd "$out" && pwd)

# 1. The installer, as released.
(cd "$inst" && shasum -a 256 -c --quiet SHA256SUMS) || die "$inst: SHA256SUMS doesn't verify"
bin=$inst/andronix-android-aarch64
[ -f "$bin" ] || die "$inst has no andronix-android-aarch64"
# Its version, from the same build's linux binary (arm64 runs natively in
# colima on Apple silicon).
version=$(docker run --rm --platform linux/arm64 -v "$inst:/i:ro" alpine:3.20 /i/andronix-linux-aarch64 version 2>/dev/null | awk '{print $2; exit}')
[ -n "$version" ] || die "can't tell the installer's version (docker run of andronix-linux-aarch64)"
commit=$(git -C "$repo" rev-parse --short HEAD)
tag=$(git -C "$repo" tag --points-at HEAD | grep -E '^v[0-9]' | head -1 || true)

# 2. proot, its loader and talloc, rebuilt and checked against the pins.
# Inside <out dir>: Docker on macOS (colima) only mounts $HOME.
case "$out" in "$HOME"/*) ;; *) die "<out dir> must be under \$HOME (Docker mounts only that)" ;; esac
pbuild=$out/.proot-build
mkdir -p "$pbuild"
trap 'rm -rf "$pbuild" "$pbuild.log"' EXIT
dl=${DL:-}
[ -n "$dl" ] || for c in "$repo/cache/sa-dl" "$repo/../sa-dl"; do [ -d "$c" ] && dl=$c && break; done
[ -n "$dl" ] || die "no source archives: set DL (see standalone/README.md)"
dl=$(cd "$dl" && pwd)
DL=$dl "$here/proot/build.sh" "$pbuild" >"$pbuild.log" 2>&1 || { cat "$pbuild.log" >&2; die "proot build failed"; }
rm -f "$pbuild.log"
if [ "${PROOT_UPDATE:-}" = 1 ]; then
    (cd "$pbuild" && shasum -a 256 libproot.so libproot-loader.so libtalloc.so) >"$here/proot/SHA256SUMS"
    echo "jnilibs: wrote standalone/proot/SHA256SUMS; commit it" >&2
fi
(cd "$pbuild" && shasum -a 256 -c --quiet "$here/proot/SHA256SUMS") ||
    die "the proot build doesn't match standalone/proot/SHA256SUMS (sources or toolchain changed?)"

# The VirGL server (the app's GPU option), the same way: rebuilt, checked
# against standalone/virgl/SHA256SUMS.
vbuild=$out/.virgl-build
mkdir -p "$vbuild"
trap 'rm -rf "$pbuild" "$pbuild.log" "$vbuild" "$vbuild.log"' EXIT
DL=$dl "$here/virgl/build.sh" "$vbuild" >"$vbuild.log" 2>&1 || { cat "$vbuild.log" >&2; die "virgl build failed"; }
if [ "${VIRGL_UPDATE:-}" = 1 ]; then
    (cd "$vbuild" && shasum -a 256 libvirgl.so libandronixpresent.so) >"$here/virgl/SHA256SUMS"
    echo "jnilibs: wrote standalone/virgl/SHA256SUMS; commit it" >&2
fi
(cd "$vbuild" && shasum -a 256 -c --quiet "$here/virgl/SHA256SUMS") ||
    die "the virgl build doesn't match standalone/virgl/SHA256SUMS (sources or toolchain changed?)"

# 3. Assemble.
abi=$out/jniLibs/arm64-v8a
mkdir -p "$abi" "$out/licenses"
cp "$bin" "$abi/libandronix.so"
cp "$pbuild/libproot.so" "$pbuild/libproot-loader.so" "$pbuild/libtalloc.so" "$vbuild/libvirgl.so" "$vbuild/libandronixpresent.so" "$abi/"
cp "$pbuild/COPYING.proot" "$pbuild/LICENSE.talloc" "$pbuild/LICENSE.libandroid-shmem" "$out/licenses/"
cp "$vbuild/COPYING.virglrenderer" "$vbuild/COPYING.libepoxy" "$out/licenses/"
cp "$here/virgl/LICENSES" "$out/licenses/LICENSES.virgl"
# The index for the app's About > Open source: each shipped library, its
# licence, the licence text here and where its source is.
cat >"$out/licenses/README.txt" <<'IDX'
Open-source components of the Andronix app's standalone mode (jniLibs/arm64-v8a).
Source and build scripts: https://github.com/AndronixApp/andronix (standalone/), and the
release standalone-<version> for the exact source archives.

libandronix.so        Andronix installer (this repository's licence)   source: the repository root
libproot.so           proot 5.1.107.96 (Termux)       GPL-2.0          COPYING.proot             source: standalone/proot, proot-5.1.107.96.zip
libproot-loader.so    proot's loader                  GPL-2.0          COPYING.proot             source: as above
libtalloc.so          talloc 2.5.0                    LGPL-3.0+        LICENSE.talloc            source: talloc-2.5.0.tar.gz
  (in libproot.so)    libandroid-shmem 0.7            BSD-3-Clause     LICENSE.libandroid-shmem  source: libandroid-shmem-0.7.tar.gz
libvirgl.so           virglrenderer 1.3.0 (+ Android patch, ours)  MIT  COPYING.virglrenderer, LICENSES.virgl  source: standalone/virgl, virglrenderer-1.3.0.tar.gz, virglrenderer-android.patch
  (in libvirgl.so)    libepoxy 1.5.10                 MIT              COPYING.libepoxy          source: libepoxy-1.5.10.tar.gz
libandronixpresent.so Andronix zero-copy present (ours)  MIT           LICENSES.virgl            source: standalone/virgl/present_jni.c
IDX
chmod 755 "$abi"/*.so
chmod 644 "$out/licenses"/*
# The sources for the public release (the NDK is linked, not copied).
mkdir -p "$out/source"
for f in proot-5.1.107.96.zip talloc-2.5.0.tar.gz libandroid-shmem-0.7.tar.gz virglrenderer-1.3.0.tar.gz libepoxy-1.5.10.tar.gz; do cp "$dl/$f" "$out/source/"; done
cp "$here/virgl/android.patch" "$out/source/virglrenderer-android.patch" # our changes to virglrenderer

# 4. Check what the app's packaging needs: arm64 ELF, and LOAD segments
# aligned to 16 KB at least (Play requires it for Android 15+ devices).
python3 - "$abi" <<'PY' || die "ELF check failed"
import struct, sys, os
bad = False
for name in sorted(os.listdir(sys.argv[1])):
    p = os.path.join(sys.argv[1], name)
    b = open(p, 'rb').read(1 << 16)
    if b[:4] != b'\x7fELF' or b[4] != 2 or b[5] != 1:
        print(f"{name}: not a 64-bit little-endian ELF"); bad = True; continue
    machine, = struct.unpack_from('<H', b, 18)
    phoff, = struct.unpack_from('<Q', b, 32)
    phentsize, phnum = struct.unpack_from('<HH', b, 54)
    if machine != 183:
        print(f"{name}: machine {machine}, not AArch64"); bad = True
    aligns = []
    for i in range(phnum):
        ptype, = struct.unpack_from('<I', b, phoff + i * phentsize)
        if ptype == 1:  # PT_LOAD
            aligns.append(struct.unpack_from('<Q', b, phoff + i * phentsize + 48)[0])
    if not aligns or min(aligns) < 16384:
        print(f"{name}: LOAD alignment {aligns}, needs 16 KB or more"); bad = True
    else:
        print(f"{name}: AArch64, LOAD align {min(aligns) // 1024} KB")
sys.exit(1 if bad else 0)
PY

(cd "$out" && find jniLibs licenses source -type f | LC_ALL=C sort | xargs shasum -a 256) >"$out/SHA256SUMS"
sum() { awk -v f="$1" '$2 == f {print $1}' "$out/SHA256SUMS"; }
# cgo: libandronix links with Android's libc (Android's resolver, card #141);
# Go records the setting in the binary's build info.
cgo=false
grep -aq 'CGO_ENABLED=1' "$out/jniLibs/arm64-v8a/libandronix.so" && cgo=true
cat >"$out/build.json" <<EOF
{
  "installer_version": "$version",
  "installer_tag": "${tag:-}",
  "repo_commit": "$commit",
  "cgo": $cgo,
  "abis": ["arm64-v8a"],
  "proot": "termux/proot 5.1.107.96, talloc 2.5.0, libandroid-shmem 0.7, NDK r27c, API 26",
  "virgl": "virglrenderer 1.3.0 (+ standalone/virgl/android.patch), libepoxy 1.5.10, NDK r27c",
  "sha256": {
    "libandronix.so": "$(sum jniLibs/arm64-v8a/libandronix.so)",
    "libproot.so": "$(sum jniLibs/arm64-v8a/libproot.so)",
    "libproot-loader.so": "$(sum jniLibs/arm64-v8a/libproot-loader.so)",
    "libtalloc.so": "$(sum jniLibs/arm64-v8a/libtalloc.so)",
    "libvirgl.so": "$(sum jniLibs/arm64-v8a/libvirgl.so)",
    "libandronixpresent.so": "$(sum jniLibs/arm64-v8a/libandronixpresent.so)"
  }
}
EOF
echo "jnilibs: installer $version${tag:+ ($tag)}, $out"
cat "$out/SHA256SUMS"
