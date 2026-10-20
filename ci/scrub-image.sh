#!/usr/bin/env bash
# Scrub a rootfs made by `andronix install --image-build` before it is
# exported as a ready-made desktop image (internal/app/image.go): run
# after ci/clean-rootfs.sh. An image holds packages only. Everything the
# installer writes per phone or per install is removed here, and written
# again on the phone after unpacking (setup, compat, desktop-setup, the
# user step), so installer changes never need a rebuild.
#
#   ci/clean-rootfs.sh R && ci/scrub-image.sh R && IMAGE=1 ci/check-image.sh R
set -euo pipefail
r=${1:?usage: scrub-image.sh <rootfs-dir>}
[ -f "$r/etc/passwd" ] || { echo "$r doesn't look like a rootfs" >&2; exit 1; }

# Ours (rootfs.Configure, InstallSelf, the family's apt settings, the
# no-snap pin): the phone writes its own.
rm -rf "$r/etc/andronix" "$r/usr/local/lib/andronix"
rm -f "$r/etc/andronix-release" "$r/etc/profile.d/andronix.sh" \
    "$r/usr/local/bin/andronix" "$r/usr/local/bin/bwrap" \
    "$r/usr/local/bin/vncserver-start" "$r/usr/local/bin/vncserver-stop" \
    "$r/etc/apt/apt.conf.d/90andronix" "$r/etc/apt/preferences.d/no-snap"

# Per machine: DNS, hosts, host name (Configure rewrites them), random
# seeds, D-Bus's machine id (clean-rootfs.sh empties /etc/machine-id).
rm -f "$r/etc/resolv.conf" "$r/etc/hosts" "$r/etc/hostname" \
    "$r/var/lib/systemd/random-seed" "$r/var/lib/urandom/random-seed" "$r/var/lib/random-seed"
[ -L "$r/var/lib/dbus/machine-id" ] || rm -f "$r/var/lib/dbus/machine-id"

# Caches that differ build to build (timestamps, inode numbers) and are
# rebuilt on use anyway: fontconfig checks them against the font folders'
# mtimes, which the tar clamps, so the phone would rebuild them regardless.
find "$r/var/cache/fontconfig" -mindepth 1 -delete 2>/dev/null || true
rm -f "$r/var/cache/ldconfig/aux-cache"
echo "scrubbed $r"
