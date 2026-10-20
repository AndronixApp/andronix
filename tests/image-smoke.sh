#!/usr/bin/env bash
# Smoke-install a freshly built ready-made image before it is published:
# a free install from it, in the Termux-like test box (tests/Dockerfile),
# exactly as a phone would (ANDRONIX_IMAGE, the local-file path of
# internal/app/image.go). Passes when the install finishes from the image
# (SOURCE=image, PREBUILT=image, STAGE=done) and the desktop's session
# command is there.
#
#   tests/image-smoke.sh <distro> <desktop>      uses dist/images/<id>-<ver>-<de>-aarch64.json
set -u
cd "$(dirname "$0")/.." || exit 1
distro=${1:?usage: image-smoke.sh <distro> <desktop>}
de=${2:?usage: image-smoke.sh <distro> <desktop>}
version=$(sed -n 's/^DISTRO_VERSION="\{0,1\}\([^"]*\)"\{0,1\}/\1/p' "distros/$distro.conf")
ptr="dist/images/$distro-$version-$de-aarch64.json"
[ -f "$ptr" ] || { echo "no $ptr" >&2; exit 1; }
img="dist/images/$(basename "$(sed -n 's/.*"url":"\([^"]*\)".*/\1/p' "$ptr")")"
[ -f "$img" ] && [ -f "$img.sha256" ] || { echo "no $img (+ .sha256)" >&2; exit 1; }
session=$(sed -n 's/^DE_SESSION="\{0,1\}\([^" ]*\).*/\1/p' "desktops/$de.conf")

box="image-smoke-$distro-$de-$$"
docker build -q -t andronix-test -f tests/Dockerfile tests >/dev/null || exit 1
docker rm -f "$box" >/dev/null 2>&1
docker run -d --init --name "$box" -v "$PWD":/src:ro andronix-test sleep infinity >/dev/null || exit 1
trap 'docker rm -f "$box" >/dev/null 2>&1' EXIT
A=/home/termux/.local/bin/andronix
t0=$(date +%s)
docker exec -e ANDRONIX_NO_TELEMETRY=1 "$box" bash -c "ANDRONIX_SRC=/src sh /src/get.sh >/dev/null && \
    ANDRONIX_IMAGE=/src/$img $A install $distro --de $de --yes --no-start --no-user --plain" || {
    echo "FAIL: install exited $?"; exit 1; }
conf=$(docker exec "$box" cat "/home/termux/.andronix/distros/$distro/install.conf")
echo "$conf" | grep -E '^(SOURCE|FALLBACK|PREBUILT|STAGE)='
fail=0
for want in SOURCE=image PREBUILT=image STAGE=done; do
    echo "$conf" | grep -qx "$want" || { echo "FAIL: want $want"; fail=1; }
done
docker exec "$box" sh -c "test -x /home/termux/.andronix/distros/$distro/rootfs/usr/bin/$session" ||
    { echo "FAIL: no /usr/bin/$session in the installed rootfs"; fail=1; }
[ "$fail" = 0 ] && echo "PASS: $distro $de installed from $(basename "$img") in $(( $(date +%s) - t0 )) s"
exit "$fail"
