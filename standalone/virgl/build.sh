#!/usr/bin/env bash
# Builds libvirgl.so for jniLibs/arm64-v8a into $1 (default ./out), from the
# pinned sources in $DL (default ./sources: android-ndk-r27c-linux.zip,
# virglrenderer-1.3.0.tar.gz, libepoxy-1.5.10.tar.gz; inside.sh checks their
# sha256). See README.md.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
out=$(mkdir -p "${1:-$here/out}" && cd "${1:-$here/out}" && pwd)
dl=$(cd "${DL:-$here/sources}" && pwd)
docker build -q --platform linux/amd64 -t andronix-virgl-build "$here" >/dev/null
docker run --rm --platform linux/amd64 -v "$dl:/dl:ro" -v "$out:/out" andronix-virgl-build ||
    { rc=$?; echo "virgl build: the container exited with $rc (137: killed, often out of memory or disk)" >&2; exit $rc; }
