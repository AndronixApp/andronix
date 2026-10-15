#!/usr/bin/env bash
# The installer in Play Store Termux (com.termux googleplay.*: binaries run
# through Android's linker, targetSdk 35+). Most apt_hook and package-script
# failures in telemetry come from there (support #147), and the GitHub
# Termux on the other AVDs never shows them. The release skill's Play leg.
#
#   tests/emulator/play-termux.sh -s SERIAL [--snapshot NAME] [--bin FILE]
#       [--distros "ubuntu debian"] [--de xfce] [--keep]
#
#   -s SERIAL     the emulator (andronix-a16-play: Play Termux installed from
#                 the APKs pulled from a phone, see tests/emulator/README.md)
#   --snapshot    load this AVD snapshot first (play-termux-fresh: Termux
#                 bootstrapped, nothing else)
#   --bin FILE    install this andronix-android-aarch64 instead of get.sh's
#   --distros     default: ubuntu debian
#   --de          default: xfce
#   --keep        don't remove a distro after it passes
#
# Play Termux isn't debuggable, so app-context.sh (run-as) can't drive it.
# Each step is a script pushed to /data/local/tmp (the app can read a file
# there by name), typed into TermuxActivity as `sh <file>`, and its output
# goes to logcat line by line (/system/bin/log, one tag per step), which is
# where this script reads it. Telemetry is off for these installs.
# Prints PASS/FAIL per distro; logs in $OUT (default cache/play-<time>).
set -uo pipefail
main() {
here=$(cd "$(dirname "$0")" && pwd)
serial= snap= bin= distros="ubuntu debian" de=xfce keep=
while [ $# -gt 0 ]; do
    case "$1" in
        -s|--serial) serial=$2; shift ;;
        --snapshot) snap=$2; shift ;;
        --bin) bin=$2; shift ;;
        --distros) distros=$2; shift ;;
        --de) de=$2; shift ;;
        --keep) keep=1 ;;
        *) echo "unknown option $1" >&2; exit 2 ;;
    esac
    shift
done
[ -n "$serial" ] || { echo "usage: $0 -s SERIAL [--snapshot NAME] [--bin FILE] [--distros \"a b\"] [--de DE] [--keep]" >&2; exit 2; }
case "$serial" in emulator-*) ;; *) echo "refusing $serial: emulators only" >&2; exit 2 ;; esac
A="adb -s $serial"
OUT=${OUT:-$here/../../cache/play-$(date +%m%d-%H%M)}
mkdir -p "$OUT"
log() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$OUT/play.log" >&2; }

if [ -n "$snap" ]; then
    $A emu avd snapshot load "$snap" | grep -q OK || { echo "snapshot $snap didn't load" >&2; exit 1; }
    $A wait-for-device
fi
ver=$($A shell dumpsys package com.termux | sed -n 's/.*versionName=//p' | head -1 | tr -d '\r')
case "$ver" in googleplay.*) ;; *) echo "com.termux on $serial is '$ver', not Play Store Termux" >&2; exit 2 ;; esac
log "Play Termux $ver on $serial ($($A emu avd name | head -1 | tr -d '\r')), Android $($A shell getprop ro.build.version.release | tr -d '\r')"

# step NAME CAP: the script on stdin, run in TermuxActivity; output to
# $OUT/NAME.log (and stdout); fails if it didn't finish within CAP seconds.
step() {
    local name=$1 cap=$2 id=PT$RANDOM$RANDOM f t
    f=/data/local/tmp/$id.sh t=$(mktemp)
    { echo "{"; cat; echo; echo "echo PT-EXIT=\$?"; echo "} 2>&1 | while IFS= read -r l; do /system/bin/log -t $id \"\$l\"; done"; } >"$t"
    $A push "$t" "$f" >/dev/null 2>&1 && $A shell chmod 644 "$f"
    rm -f "$t"
    $A shell input keyevent KEYCODE_WAKEUP
    $A shell am start -n com.termux/.app.TermuxActivity >/dev/null 2>&1
    sleep 2
    $A shell input text "sh%s$f"
    $A shell input keyevent KEYCODE_ENTER
    local end=$(( $(date +%s) + cap ))
    while [ "$(date +%s)" -lt "$end" ]; do
        $A logcat -d -s "$id:I" 2>/dev/null | grep -q "PT-EXIT=" && break
        sleep 10
    done
    $A logcat -d -v raw -s "$id:I" 2>/dev/null | grep -v '^--------- beginning' | tr -d '\r' >"$OUT/$name.log"
    $A shell rm -f "$f"
    cat "$OUT/$name.log"
    grep -q "PT-EXIT=" "$OUT/$name.log" || { echo "STEP TIMEOUT ($cap s)" | tee -a "$OUT/$name.log"; return 1; }
}

if [ -n "$bin" ]; then
    $A push "$bin" /data/local/tmp/andronix-play >/dev/null && $A shell chmod 644 /data/local/tmp/andronix-play
    step setup 300 <<'X' | tail -2
command -v proot >/dev/null || pkg install -y proot >/dev/null 2>&1
B=$PREFIX/bin/andronix; rm -f $B; cp /data/local/tmp/andronix-play $B && chmod 700 $B
andronix version
X
    $A shell rm -f /data/local/tmp/andronix-play
else
    step setup 600 <<'X' | tail -2
curl -fsSL https://dl.andronix.app/get.sh -o $PREFIX/tmp/get.sh && sh $PREFIX/tmp/get.sh >/dev/null 2>&1
andronix version
X
fi
log "installer: $(grep -a '^andronix ' "$OUT/setup.log" | head -1)"
grep -aq '^andronix ' "$OUT/setup.log" || { log "the installer didn't install"; exit 1; }

pass=() fail=()
for d in $distros; do
    log "== $d --de $de"
    step "$d" 2700 <<X >/dev/null
export ANDRONIX_NO_TELEMETRY=1 ANDRONIX_USER_PASSWORD=tester123 ANDRONIX_VNC_PASSWORD=tester123
andronix install $d --de $de --user tester --no-start --yes >\$PREFIX/tmp/pt-install.out 2>&1
grep -a " is ready\|^error:\|^fail:\|^fix:" \$PREFIX/tmp/pt-install.out
tail -8 \$PREFIX/tmp/pt-install.out
f=\$(ls -t \$HOME/.andronix/logs/install-$d-*.log 2>/dev/null | head -1)
echo "--- \$f"; grep -a "step:\|attempt\|E: \|Trying again\|hook\|fail" "\$f" | tail -40
X
    if grep -aq " is ready" "$OUT/$d.log"; then
        pass+=("$d"); log "$d PASS"
        [ -n "$keep" ] || step "$d-remove" 300 >/dev/null <<X
andronix remove $d --yes >/dev/null 2>&1; echo removed
X
    else
        fail+=("$d"); log "$d FAIL: $(grep -a '^error:\|^fail:' "$OUT/$d.log" | head -2 | tr '\n' ' ')"
    fi
done
log "PASS ${#pass[@]}: ${pass[*]:-}"
log "FAIL ${#fail[@]}: ${fail[*]:-}"
[ ${#fail[@]} -eq 0 ]
}
main "$@"
