#!/usr/bin/env bash
# Upload build output to the PUBLIC Andronix bucket (andronix-dl, served at
# https://dl.andronix.app). Only free artefacts may ever go there; paid
# Modded images never go there.
#
#   ci/publish-r2.sh rootfs <distro>     dist/<id>-<ver>-<arch>.tar.xz(.sha256, .meta)
#   ci/publish-r2.sh bin <version>       dist/andronix-{android-,linux-,}<arch> + SHA256SUMS -> bin/<version>/ and bin/latest/
#                                        (NO_LATEST=1: bin/<version>/ only, for a release candidate)
#   ci/publish-r2.sh getsh [version]     get.sh -> get.sh (short cache; the app and docs fetch it)
#   ci/publish-r2.sh image <distro> <de> dist/images/<id>-<ver>-<de>-aarch64-<date>.tar.zst (+ .sha256, .meta,
#                                        .manifest) -> rootfs/<id>/<ver>/, then the pointer <id>-<ver>-<de>-aarch64.json
#                                        LAST (it is what installers read; deleting it turns the image off),
#                                        then deletes dated images older than the previous one.
#
# Env: R2_ACCOUNT_ID, AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY (a token
# scoped to this bucket). DRY_RUN=1 prints what would be uploaded.
#
# Guard: every object key must match the free allowlist below, and no key,
# file name or file content marker may look paid/modded. Anything else
# stops the whole upload before a single byte is sent.
set -euo pipefail
cd "$(dirname "$0")/.."

BUCKET=andronix-dl            # public; nothing else is ever uploaded
usage="usage: publish-r2.sh rootfs <distro> | bin <version> | getsh | image <distro> <de>"
kind=${1:?$usage}
arg=${2:-}
[ -n "$arg" ] || [ "$kind" = getsh ] || { echo "$usage" >&2; exit 1; }

allowed() {
    local key=$1
    case "$key" in
        *modded*|*Modded*|*paid*|*premium*) return 1 ;;
    esac
    # Versions: numbers, or rolling/stable (Kali, Arch, Void, Manjaro).
    [[ $key =~ ^rootfs/[a-z0-9]+/([0-9.]+|rolling|stable)/[a-z0-9]+-([0-9.]+|rolling|stable)-(aarch64|arm|i686|x86_64)\.(tar\.xz|tar\.xz\.sha256|meta)$ ]] && return 0
    # Ready-made desktop images: dated files (never overwritten) and the undated pointer.
    [[ $key =~ ^rootfs/[a-z0-9]+/([0-9.]+|rolling|stable)/[a-z0-9]+-([0-9.]+|rolling|stable)-[a-z0-9]+-aarch64-[0-9]{8}\.(tar\.zst|tar\.zst\.sha256|meta|manifest)$ ]] && return 0
    [[ $key =~ ^rootfs/[a-z0-9]+/([0-9.]+|rolling|stable)/[a-z0-9]+-([0-9.]+|rolling|stable)-[a-z0-9]+-aarch64\.json$ ]] && return 0
    [ "$key" = get.sh ] && return 0
    [[ $key =~ ^bin/(latest|[0-9]+\.[0-9]+\.[0-9]+([-.][a-z0-9.]+)?)/(andronix-((android|linux)-)?(aarch64|arm|i686|x86_64)|SHA256SUMS)$ ]] && return 0
    return 1
}

# Build the upload plan: "local-file key cache-control", checksums last.
plan=()
case "$kind" in
    rootfs)
        conf="distros/$arg.conf"
        [ -f "$conf" ] || { echo "no $conf" >&2; exit 1; }
        grep -qiE '^(DISTRO_MODDED|DISTRO_PAID)=' "$conf" && { echo "refusing: $conf is marked paid/modded" >&2; exit 1; }
        # shellcheck source=/dev/null
        . "$conf"
        shopt -s nullglob
        for f in dist/"$DISTRO_ID-$DISTRO_VERSION"-*.tar.xz dist/"$DISTRO_ID-$DISTRO_VERSION"-*.meta; do
            plan+=("$f rootfs/$DISTRO_ID/$DISTRO_VERSION/$(basename "$f") public,max-age=3600")
        done
        for f in dist/"$DISTRO_ID-$DISTRO_VERSION"-*.tar.xz.sha256; do
            plan+=("$f rootfs/$DISTRO_ID/$DISTRO_VERSION/$(basename "$f") public,max-age=300")
        done
        ;;
    bin)
        dests="$arg latest"
        [ -n "${NO_LATEST:-}" ] && dests=$arg
        for dest in $dests; do
            # andronix-android-<cpu> (Termux), andronix-linux-<cpu> (inside
            # distros, and off Android), andronix-<cpu> (the android build
            # under the old name, for older get.sh and self-updates).
            for cpu in aarch64 arm i686 x86_64; do
                for f in dist/andronix-android-$cpu dist/andronix-linux-$cpu dist/andronix-$cpu; do
                    [ -f "$f" ] && plan+=("$f bin/$dest/$(basename "$f") public,max-age=300")
                done
            done
            plan+=("dist/SHA256SUMS bin/$dest/SHA256SUMS public,max-age=300")
        done
        ;;
    image)
        de=${3:?$usage}
        conf="distros/$arg.conf"
        [ -f "$conf" ] || { echo "no $conf" >&2; exit 1; }
        grep -qiE '^(DISTRO_MODDED|DISTRO_PAID)=' "$conf" && { echo "refusing: $conf is marked paid/modded" >&2; exit 1; }
        # shellcheck source=/dev/null
        . "$conf"
        ptr="dist/images/$DISTRO_ID-$DISTRO_VERSION-$de-aarch64.json"
        [ -f "$ptr" ] || { echo "no $ptr" >&2; exit 1; }
        img=$(sed -n 's/.*"url":"\([^"]*\)".*/\1/p' "$ptr")
        f="dist/images/$(basename "$img")"
        d="rootfs/$DISTRO_ID/$DISTRO_VERSION"
        [ "$img" = "$d/$(basename "$img")" ] || { echo "refusing: the pointer names $img, outside $d/" >&2; exit 1; }
        # The pointer's checksum must be the image's own.
        want=$(sed -n 's/.*"sha256":"\([0-9a-f]*\)".*/\1/p' "$ptr")
        [ "$want" = "$(awk '{ print $1 }' "$f.sha256")" ] || { echo "refusing: $ptr and $f.sha256 disagree" >&2; exit 1; }
        for x in "$f" "$f.sha256" "${f%.tar.zst}.meta" "${f%.tar.zst}.manifest"; do
            plan+=("$x $d/$(basename "$x") public,max-age=31536000,immutable")
        done
        plan+=("$ptr $d/$(basename "$ptr") public,max-age=300")   # last
        ;;
    getsh)
        # It downloads bin/latest; it must not point anywhere else.
        grep -q '/bin/latest}$' get.sh || { echo "refusing: get.sh doesn't default to bin/latest" >&2; exit 1; }
        sh -n get.sh || { echo "refusing: get.sh has a syntax error" >&2; exit 1; }
        plan+=("get.sh get.sh public,max-age=300")
        ;;
    *) echo "unknown kind $kind" >&2; exit 1 ;;
esac
[ ${#plan[@]} -gt 0 ] || { echo "nothing to upload" >&2; exit 1; }

# Guard pass over the whole plan first.
bad=0
for p in "${plan[@]}"; do
    read -r f key _ <<<"$p"
    if ! allowed "$key"; then echo "REFUSED (not a free artefact path): $key" >&2; bad=1; fi
    if [ ! -f "$f" ]; then echo "missing: $f" >&2; bad=1; fi
done
[ "$bad" = 0 ] || { echo "upload stopped; nothing was sent" >&2; exit 1; }

# Rootfs and images must pass the hygiene gate right before upload, too.
if [ "$kind" = rootfs ] || [ "$kind" = image ]; then
    for p in "${plan[@]}"; do
        read -r f _ _ <<<"$p"
        case "$f" in
            *.tar.xz) bash ci/check-image.sh "$f" >/dev/null || { echo "hygiene check failed: $f" >&2; exit 1; } ;;
            *.tar.zst) IMAGE=1 bash ci/check-image.sh "$f" >/dev/null || { echo "hygiene check failed: $f" >&2; exit 1; } ;;
        esac
    done
fi

ep="https://${R2_ACCOUNT_ID:?set R2_ACCOUNT_ID}.r2.cloudflarestorage.com"
# Rollback: rebuilds of a version (rolling distros, weekly refreshes)
# overwrite the same key, so keep the tarball being replaced as <key>.prev
# (a server-side copy; nothing if the key is new). See delivery.md.
if [ "$kind" = rootfs ] && [ -z "${DRY_RUN:-}" ]; then
    for p in "${plan[@]}"; do
        read -r _ key _ <<<"$p"
        case "$key" in *.tar.xz)
            aws s3 cp "s3://$BUCKET/$key" "s3://$BUCKET/$key.prev" --endpoint-url "$ep" --only-show-errors 2>/dev/null ||
                echo "no previous $key to keep (new file)" ;;
        esac
    done
fi
for p in "${plan[@]}"; do
    read -r f key cache <<<"$p"
    if [ -n "${DRY_RUN:-}" ]; then
        echo "would upload $f -> s3://$BUCKET/$key ($cache)"
    else
        aws s3 cp "$f" "s3://$BUCKET/$key" --endpoint-url "$ep" --cache-control "$cache" --only-show-errors
        echo "uploaded https://dl.andronix.app/$key"
    fi
done

# Images: keep the current and the previous dated image, delete older ones
# (with their checksum, meta and manifest). Only this image's own names.
if [ "$kind" = image ] && [ -z "${DRY_RUN:-}" ]; then
    prefix="$DISTRO_ID-$DISTRO_VERSION-$de-aarch64-"
    old=$(aws s3 ls "s3://$BUCKET/$d/" --endpoint-url "$ep" | awk '{ print $4 }' |
        grep -E "^$prefix[0-9]{8}\.tar\.zst$" | sort -r | tail -n +3)
    for o in $old; do
        for x in "$o" "$o.sha256" "${o%.tar.zst}.meta" "${o%.tar.zst}.manifest"; do
            allowed "$d/$x" || { echo "not deleting $d/$x (not an image path)" >&2; continue; }
            aws s3 rm "s3://$BUCKET/$d/$x" --endpoint-url "$ep" --only-show-errors && echo "deleted old $d/$x"
        done
    done
fi
