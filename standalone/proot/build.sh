#!/usr/bin/env bash
# Builds libproot.so, libproot-loader.so and libtalloc.so for jniLibs/arm64-v8a
# into $1 (default ./out), from the pinned sources in $DL (default ./sources:
# android-ndk-r27c-linux.zip, talloc-2.5.0.tar.gz, proot-5.1.107.96.zip,
# libandroid-shmem-0.7.tar.gz; inside.sh checks their sha256). See
# ../README.md.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=$(mkdir -p "${1:-$here/out}" && cd "${1:-$here/out}" && pwd)
dl=$(cd "${DL:-$here/sources}" && pwd)
docker build -q --platform linux/amd64 -t andronix-proot-build "$here" >/dev/null
docker run --rm --platform linux/amd64 -e PROOT_STOPS="${PROOT_STOPS:-}" -e PROOT_PLAIN="${PROOT_PLAIN:-}" -v "$dl:/dl:ro" -v "$here:/dl-stops:ro" -v "$out:/out" andronix-proot-build ||
    { rc=$?; echo "proot build: the container exited with $rc (137: killed, often out of memory or disk)" >&2; exit $rc; }
