#!/usr/bin/env bash
# The standalone app's path, distro by distro, on an emulator that has
# Termux: andronix runs inside the Termux app process (app-context.sh, so
# the app's seccomp filter and SELinux domain) but as the Andronix app runs
# it: ANDRONIX_HOST=standalone, proot and its loader from a jniLibs folder,
# its own ANDRONIX_HOME, no Termux packages.
#
#   tests/emulator/standalone-matrix.sh -s SERIAL --jnilibs DIR [--distros "a b"] [--keep]
#
#   --jnilibs DIR   standalone/jnilibs.sh output (DIR/jniLibs/arm64-v8a/*.so)
#   --distros       default: every distros/*.conf
#   --keep          don't remove a distro after it passes
#   --beta FILE     the app's beta installer (libandronix_beta.so, card #209):
#                   it installs each distro, then the stable libandronix.so
#                   runs every later check, as when a user moves a distro
#                   from the beta back to stable; plus a check that the
#                   distro's own andronix became the stable one
#   --env 'K=V ...' extra environment for every job, e.g. a local image
#                   mirror: --env 'ANDRONIX_MIRROR=http://127.0.0.1:8899'
#                   (with adb reverse), or 'ANDRONIX_NO_IMAGE=1'
#
# For each distro:
#   1. install <d> --de xfce --user tester --progress json  (done status ok)
#   2. display <d> --size 1600x720: SOUND then DISPLAY ready lines
#   3. ctl socket: H 1600 720, then R 720 1600 gives H 720 1600 (same stride)
#   4. PCM socket: sound bytes flow
#   5. clipboard: the app's C reaches CLIPBOARD (xclip in the distro)
#   6. display stop, then remove <d> (unless --keep)
# Prints PASS/FAIL per distro; logs in $OUT (default cache/sa-matrix-<time>).
set -uo pipefail
# All in main: the file is read whole before anything runs, so editing it
# during a run can't change what runs.
main() {
here=$(cd "$(dirname "$0")" && pwd)
serial= jni= keep= distros= beta=
while [ $# -gt 0 ]; do
    case "$1" in
        -s|--serial) serial=$2; shift ;;
        --jnilibs) jni=$2; shift ;;
        --distros) distros=$2; shift ;;
        --keep) keep=1 ;;
        --beta) beta=$2; shift ;;
        --env) extra_env=$2; shift ;;
        *) echo "unknown option $1" >&2; exit 2 ;;
    esac
    shift
done
[ -n "$serial" ] && [ -n "$jni" ] || { echo "usage: $0 -s SERIAL --jnilibs DIR [--distros \"a b\"] [--keep] [--beta FILE]" >&2; exit 2; }
lib=$jni/jniLibs/arm64-v8a
[ -f "$lib/libandronix.so" ] || { echo "no $lib/libandronix.so" >&2; exit 2; }
[ -n "$distros" ] || distros=$(cd "$here/../../distros" && ls *.conf | sed 's/\.conf$//' | tr '\n' ' ')
OUT=${OUT:-$here/../../cache/sa-matrix-$(date +%m%d-%H%M)}
mkdir -p "$OUT"
ADB="adb -s $serial"
case "$serial" in emulator-*) ;; *) [ "${ALLOW_REAL_DEVICE:-}" = 1 ] || { echo "refusing a real device without ALLOW_REAL_DEVICE=1" >&2; exit 2; } ;; esac
log() { printf '%s  %s\n' "$(date +%H:%M:%S)" "$*" | tee -a "$OUT/matrix.log" >&2; }

# X's abstract sockets (@/tmp/.X11-unix/X1) are device-wide: an X server
# of the Andronix app itself (or anything else) on :1 makes every display
# check fail ("Cannot establish any listening sockets"; Oct 7, the app's
# desktop left from an in-app test). Stop the app, then insist on none.
$ADB shell am force-stop studio.com.techriz.andronix >/dev/null 2>&1
sleep 1
if $ADB shell "ps -A -o NAME" | grep -qi '^\(Xvfb\|Xvnc\|Xtigervnc\)$'; then
    echo "an X server is already running on $serial (another app's desktop?): stop it first" >&2
    $ADB shell "ps -A -o PID,USER,NAME" | grep -i 'xvfb\|xvnc' >&2
    exit 1
fi

# The libraries, the way the app has them: one folder, exec allowed.
TH=/data/data/com.termux/files/home
# By name: the app's uid can't list /data/local/tmp, so a glob there is empty.
$ADB shell "run-as com.termux sh -c 'rm -rf $TH/sa-lib && mkdir -p $TH/sa-lib'"
for f in "$lib"/*.so ${beta:+"$beta"}; do
    n=$(basename "$f")
    [ "$f" = "${beta:-}" ] && n=libandronix_beta.so
    $ADB push "$f" "/data/local/tmp/sa-$n" >/dev/null || exit 1
    $ADB shell "chmod 644 /data/local/tmp/sa-$n; run-as com.termux sh -c 'cp /data/local/tmp/sa-$n $TH/sa-lib/$n && chmod 700 $TH/sa-lib/$n'; rm -f /data/local/tmp/sa-$n"
done
[ "$($ADB shell "run-as com.termux ls $TH/sa-lib" | tr -d '\r' | grep -c '\.so$')" = "$(( $(ls "$lib"/*.so | wc -l) + $([ -n "$beta" ] && echo 1 || echo 0) ))" ] ||
    { echo "copying the libraries into Termux failed" >&2; exit 1; }
log "libraries: $(cd "$lib" && shasum -a 256 libandronix.so | cut -c1-12) libandronix.so${beta:+, beta $(shasum -a 256 "$beta" | cut -c1-12) installs}, serial $serial"

# The environment every job starts with.
env_lines='L=$HOME/sa-lib; export ANDRONIX_HOST=standalone ANDRONIX_HOME=$HOME/sa-home/andronix PROOT_BIN=$L/libproot.so PROOT_LOADER=$L/libproot-loader.so PROOT_TMP_DIR=$HOME/sa-home/tmp LD_LIBRARY_PATH=$L ANDRONIX_NO_TELEMETRY=1
mkdir -p $HOME/sa-home/tmp; A=$L/libandronix.so; I=$A'
[ -n "$beta" ] && env_lines="$env_lines
I=\$L/libandronix_beta.so"
[ -n "${extra_env:-}" ] && env_lines="$env_lines
export $extra_env"

job() { # job NAME [VAR=value...]: the script on stdin, in the app context; output to $OUT/NAME.log
    local name=$1; shift
    { echo "$env_lines"; for v in "$@"; do echo "$v"; done; cat; } | "$here/app-context.sh" -s "$serial" >"$OUT/$name.log" 2>&1
}

job selfcheck <<'X'
$A selfcheck --json; echo "selfcheck exit $?"
X
grep -q '"ok":true' "$OUT/selfcheck.log" && log "selfcheck ok" || log "selfcheck FAILED (see selfcheck.log)"

pass=() fail=()
for d in $distros; do
    log "== $d"
    job "$d" "d=$d" <<'X'
$A display stop >/dev/null 2>&1
check() { if eval "$2"; then echo "CHECK ok   $1"; else echo "CHECK FAIL $1"; fi; }
t0=$(date +%s)
ANDRONIX_USER_PASSWORD=tester123 $I install $d --de xfce --user tester --progress json 2>$HOME/sa-m-$d.err >$HOME/sa-m-$d.json
check install 'grep -q "\"t\":\"done\"" $HOME/sa-m-$d.json && grep -q "\"status\":\"ok\"" $HOME/sa-m-$d.json'
echo "install: $(( $(date +%s) - t0 )) s"; tail -c 600 $HOME/sa-m-$d.err | tail -3
# The distro's own andronix is the one starting it (after a beta install: stable's, card #209).
hv=$($A start $d -- andronix version 2>/dev/null | awk '{print $2; exit}'); sv=$($A version | awk '{print $2; exit}')
echo "in-distro andronix: $hv, host: $sv"
check helper-matches-host '[ -n "$hv" ] && [ "$hv" = "$sv" ]'
echo "source: $(grep -E '^(SOURCE|FALLBACK|PREBUILT)=' $ANDRONIX_HOME/distros/$d/install.conf | tr '\n' ' ')"
r=$ANDRONIX_HOME/distros/$d/rootfs
echo "xfwm4 system defaults: $([ -s $r/etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml ] && [ -s $r/etc/andronix/xfwm4-defaults.sha256 ] && echo present || echo missing)"
($A display $d --size 1600x720 > $HOME/sa-m-disp.log 2>&1 &)
for i in $(seq 1 300); do grep -q "DISPLAY ready\|rror" $HOME/sa-m-disp.log && break; sleep 1; done
echo "display: ${i} s"; grep -a "SOUND\|DISPLAY\|rror" $HOME/sa-m-disp.log
check display 'grep -q "DISPLAY ready" $HOME/sa-m-disp.log'
check sound-line 'grep -A1 "^SOUND ready" $HOME/sa-m-disp.log | grep -q "DISPLAY ready"'
C=$ANDRONIX_HOME/d/1.sock; T=$HOME/sa-m-ctl; rm -rf $T; mkdir -p $T
mkfifo $T/in; (cat $T/in | /system/bin/nc -U $C > $T/out 2>&1 &); exec 3>$T/in; sleep 2
echo "R 720 1600" >&3; sleep 3
grep -a "^H" $T/out
strides=$(grep -a "^H" $T/out | awk '{print $4}' | sort -u | wc -l)
check H-start 'grep -aq "^H 1600 720 " $T/out'
check R-portrait 'grep -aq "^H 720 1600 " $T/out && [ "$strides" = 1 ]'
n=$(timeout 3 /system/bin/nc -U $ANDRONIX_HOME/d/snd 2>/dev/null | wc -c); echo "pcm bytes in 3 s: $n"
check pcm '[ "$n" -gt 200000 ]'
$A start --root $d -- sh -c 'command -v xclip >/dev/null || { apt-get install -y xclip || dnf install -y xclip || pacman -S --noconfirm xclip || apk add xclip || xbps-install -y xclip; } >/dev/null 2>&1' >/dev/null 2>&1
echo "C $(printf 'andronix-clip-ok' | base64 -w0)" >&3; sleep 2
clip=$($A start $d -- sh -c 'DISPLAY=:1 xclip -o -selection clipboard' 2>/dev/null)
echo "clipboard: $clip"
check clipboard '[ "$clip" = andronix-clip-ok ]'
x=$ANDRONIX_HOME/distros/$d/rootfs/home/tester/.config/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml
check compositing-off '! grep -q "use_compositing\" type=\"bool\" value=\"true" "$x" 2>/dev/null'
exec 3>&-
$A display stop >/dev/null 2>&1
X
    res=$(grep -a '^CHECK' "$OUT/$d.log")
    if [ -n "$res" ] && ! grep -q 'CHECK FAIL' <<<"$res"; then
        pass+=("$d"); log "$d PASS ($(grep -a '^install:' "$OUT/$d.log"))"
        [ -n "$keep" ] || job "$d-remove" "d=$d" <<'X'
$A remove $d --yes >/dev/null 2>&1; echo removed
X
    else
        fail+=("$d"); log "$d FAIL: $(grep -a 'CHECK FAIL' <<<"$res" | cut -c12- | tr '\n' ' ')$([ -n "$res" ] || echo "no checks ran")"
    fi
done
log "PASS ${#pass[@]}: ${pass[*]:-}"
log "FAIL ${#fail[@]}: ${fail[*]:-}"
[ ${#fail[@]} -eq 0 ]
}
main "$@"
