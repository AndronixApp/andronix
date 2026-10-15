#!/usr/bin/env bash
# Assembles the app's beta installer drop (card #209, Route A): the app
# bundles a second installer, libandronix_beta.so, next to the stable
# drop's libraries. Only the installer differs: proot, its loader, talloc
# and virgl are the stable drop's, shared by both installers.
#
#   standalone/jnilibs-beta.sh <beta installer dir> <stable drop dir> <out dir>
#
# <beta installer dir> is a beta installer build: the files of
#   VERSION=x.y.z-betaN ci/build-go.sh plus SHA256SUMS (cache/rc-<version>).
#   libandronix_beta.so is its andronix-android-aarch64, byte for byte.
# <stable drop dir> is the jnilibs.sh output the same app release bundles
#   (cache/jnilibs-<stable version>); it must verify, and its licences and
#   source archives are copied, since the beta runs on the same libraries.
# <out dir> gets:
#   jniLibs-beta/arm64-v8a/libandronix_beta.so   for the app (copy as is)
#   licenses/, source/                           the stable drop's
#   SHA256SUMS                                   of everything above
#   build.json                                   channel beta, versions, sums,
#                                                and the stable drop it pairs with
#
# Nothing is uploaded or published. The public source release is
# standalone-<beta version> (prerelease), next to the stable drop's.
set -euo pipefail
here=$(cd "$(dirname "$0")" && pwd)
repo=$(cd "$here/.." && pwd)
die() { echo "jnilibs-beta: $*" >&2; exit 1; }

[ $# -eq 3 ] || die "usage: standalone/jnilibs-beta.sh <beta installer dir> <stable drop dir> <out dir>"
inst=$(cd "$1" && pwd) || die "no installer dir $1"
stable=$(cd "$2" && pwd) || die "no stable drop $2"
out=$3
[ ! -e "$out" ] || [ -z "$(ls -A "$out")" ] || die "$out isn't empty"
mkdir -p "$out" && out=$(cd "$out" && pwd)

(cd "$inst" && shasum -a 256 -c --quiet SHA256SUMS) || die "$inst: SHA256SUMS doesn't verify"
(cd "$stable" && shasum -a 256 -c --quiet SHA256SUMS) || die "$stable: SHA256SUMS doesn't verify"
[ -f "$stable/build.json" ] && [ -f "$stable/jniLibs/arm64-v8a/libproot.so" ] || die "$stable isn't a jnilibs.sh drop"
bin=$inst/andronix-android-aarch64
[ -f "$bin" ] || die "$inst has no andronix-android-aarch64"
version=$(docker run --rm --platform linux/arm64 -v "$inst:/i:ro" alpine:3.20 /i/andronix-linux-aarch64 version 2>/dev/null | awk '{print $2; exit}')
[ -n "$version" ] || die "can't tell the installer's version (docker run of andronix-linux-aarch64)"
stable_version=$(sed -n 's/.*"installer_version": "\([^"]*\)".*/\1/p' "$stable/build.json")
[ "$version" != "$stable_version" ] || die "the beta ($version) is the same version as the stable drop"
case $version in *-beta[0-9]*|*-rc[0-9]*|*-dev[0-9]*) ;; *) die "$version isn't a pre-release version (x.y.z-betaN): a plain x.y.z sends release telemetry" ;; esac
commit=$(git -C "$repo" rev-parse --short HEAD)

abi=$out/jniLibs-beta/arm64-v8a
mkdir -p "$abi"
cp "$bin" "$abi/libandronix_beta.so"
chmod 755 "$abi/libandronix_beta.so"
cp -R "$stable/licenses" "$stable/source" "$out/"

python3 - "$abi/libandronix_beta.so" <<'PY' || die "ELF check failed"
import struct, sys
p = sys.argv[1]
b = open(p, 'rb').read(1 << 16)
assert b[:4] == b'\x7fELF' and b[4] == 2 and b[5] == 1, "not a 64-bit little-endian ELF"
assert struct.unpack_from('<H', b, 18)[0] == 183, "not AArch64"
phoff, = struct.unpack_from('<Q', b, 32)
phentsize, phnum = struct.unpack_from('<HH', b, 54)
aligns = [struct.unpack_from('<Q', b, phoff + i * phentsize + 48)[0] for i in range(phnum)
          if struct.unpack_from('<I', b, phoff + i * phentsize)[0] == 1]
assert aligns and min(aligns) >= 16384, f"LOAD alignment {aligns}, needs 16 KB or more"
print(f"libandronix_beta.so: AArch64, LOAD align {min(aligns) // 1024} KB")
PY

(cd "$out" && find jniLibs-beta licenses source -type f | LC_ALL=C sort | xargs shasum -a 256) >"$out/SHA256SUMS"
# SHA256SUMS lists this folder's files only (it must verify); the shared
# libraries' sums, from the stable drop, go into build.json.
sum() { awk -v f="$1" '$2 == f {print $1}' "$out/SHA256SUMS" "$stable/SHA256SUMS" | head -1; }
cgo=false
grep -aq 'CGO_ENABLED=1' "$abi/libandronix_beta.so" && cgo=true
cat >"$out/build.json" <<EOF
{
  "channel": "beta",
  "installer_version": "$version",
  "repo_commit": "$commit",
  "cgo": $cgo,
  "abis": ["arm64-v8a"],
  "pairs_with": {
    "installer_version": "$stable_version",
    "libandronix.so": "$(awk '$2 == "jniLibs/arm64-v8a/libandronix.so" {print $1}' "$stable/SHA256SUMS")"
  },
  "sha256": {
    "libandronix_beta.so": "$(sum jniLibs-beta/arm64-v8a/libandronix_beta.so)",
    "libproot.so": "$(sum jniLibs/arm64-v8a/libproot.so)",
    "libproot-loader.so": "$(sum jniLibs/arm64-v8a/libproot-loader.so)",
    "libtalloc.so": "$(sum jniLibs/arm64-v8a/libtalloc.so)",
    "libvirgl.so": "$(sum jniLibs/arm64-v8a/libvirgl.so)",
    "libandronixpresent.so": "$(sum jniLibs/arm64-v8a/libandronixpresent.so)"
  }
}
EOF
echo "jnilibs-beta: installer $version (beta), pairs with stable $stable_version, $out"
cat "$out/SHA256SUMS"
