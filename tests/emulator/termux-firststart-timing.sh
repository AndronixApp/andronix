#!/usr/bin/env bash
# The first XFCE start in a new home, Termux path (VNC), with and without the
# xfwm4 defaults channel (xfwm4defaults.go): the system xfwm4.xml and the light
# layer's copy are moved aside for "off". Times vncserver-start to xfdesktop's
# window being viewable, ROUNDS times each, on a fresh home every time.
#
#   tests/emulator/termux-firststart-timing.sh -s SERIAL [--src DIR] [--distro debian] [--rounds 2]
#
#   --src DIR   an installer checkout with dist/andronix-*-aarch64 (ci/build-go.sh);
#               without it, the andronix already in Termux is used
# Needs Termux (debuggable) on the emulator; runs inside the Termux app (app-context.sh).
set -uo pipefail
here=$(cd "$(dirname "$0")" && pwd)
serial="" src="" distro=debian rounds=2
while [ $# -gt 0 ]; do
	case "$1" in
	-s) serial=$2; shift ;;
	--src) src=$2; shift ;;
	--distro) distro=$2; shift ;;
	--rounds) rounds=$2; shift ;;
	*) echo "unknown option $1" >&2; exit 2 ;;
	esac
	shift
done
[ -n "$serial" ] || { echo "usage: $0 -s SERIAL [--src DIR] [--distro debian] [--rounds 2]" >&2; exit 2; }
case "$serial" in emulator-*) ;; *) echo "emulators only" >&2; exit 2 ;; esac
ADB="adb -s $serial"
tx() { "$here/app-context.sh" -s "$serial"; }
log() { echo "$(date +%T)  $*"; }

if [ -n "$src" ]; then
	stage=$(mktemp -d); tarball=$(mktemp).tar
	(cd "$src" && git archive HEAD | tar -x -C "$stage")
	mkdir -p "$stage/dist" && cp "$src"/dist/*aarch64* "$stage/dist/"
	COPYFILE_DISABLE=1 tar -cf "$tarball" -C "$stage" .
	$ADB push "$tarball" /data/local/tmp/andronix-src.tar >/dev/null && $ADB shell chmod 644 /data/local/tmp/andronix-src.tar
	rm -rf "$stage" "$tarball"
	tx <<'T'
rm -rf ~/ad && mkdir -p ~/ad && tar -xf /data/local/tmp/andronix-src.tar -C ~/ad 2>/dev/null && ANDRONIX_SRC=~/ad sh ~/ad/get.sh >/dev/null 2>&1
T
fi
log "installer: $(tx <<<'andronix version' | tail -1)"

# Install with a user and a VNC password (as test-matrix.sh does), plus the tools to look.
out=$(tx <<T
andronix install $distro --de xfce --yes --no-start >/dev/null 2>&1; echo "install=\$?"
andronix start $distro --root -- env ANDRONIX_USER_PASSWORD=tester123 ANDRONIX_VNC_PASSWORD=vncpass1 andronix setup-user --user tester >/dev/null 2>&1; echo "setup-user=\$?"
andronix start $distro --root -- sh -c 'command -v apt-get >/dev/null && DEBIAN_FRONTEND=noninteractive apt-get install -y xdotool x11-utils >/dev/null 2>&1; echo "tools=\$?"; echo "profile=\$(cat /etc/andronix/profile 2>/dev/null)"; echo "marker=\$(ls /etc/andronix/xfwm4-defaults.sha256 2>/dev/null | wc -l)"; echo "system=\$(grep -c "<property" /etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml 2>/dev/null)"; echo "light=\$(grep -c "<property" /etc/xdg/andronix-light/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml 2>/dev/null)"; echo "home=\$(ls -d /home/tester 2>/dev/null)"'
T
)
echo "$out" | sed 's/^/    /'
echo "$out" | grep -q "install=0" && echo "$out" | grep -q "setup-user=0" && echo "$out" | grep -q "tools=0" && echo "$out" | grep -q "home=/home/tester" ||
	{ log "setup failed, nothing timed"; exit 1; }

# Root moves the defaults aside ("off") or back ("on") and empties the user's home config.
toggle='S=/etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml; L=/etc/xdg/andronix-light/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml
for f in $S $L; do if [ "$1" = off ]; then [ -f $f ] && mv $f $f.off; else [ -f $f.off ] && mv $f.off $f; fi; done
cd /home/tester && rm -rf .cache .local && cd .config && ls | grep -v tigervnc | xargs rm -rf; true'
# As the user: start VNC, wait (180 s at most) for xfdesktop's window to be viewable, stop.
measure='t0=$(date +%s%3N)
vncserver-start 1280x720 :1 >/tmp/sv2-vnc.log 2>&1 || { echo "vncserver-start failed: $(tail -3 /tmp/sv2-vnc.log | tr "\n" " ")"; exit 1; }
ok=""
for i in $(seq 1 1800); do
	for w in $(DISPLAY=:1 xdotool search --classname xfdesktop 2>/dev/null); do
		DISPLAY=:1 xwininfo -id "$w" 2>/dev/null | grep -q "IsViewable" && { ok=1; break 2; }
	done
	sleep 0.1
done
t1=$(date +%s%3N)
if [ -n "$ok" ]; then echo "xfdesktop viewable after $((t1 - t0)) ms"; else echo "TIMEOUT after $((t1 - t0)) ms; session log: $(tail -5 /tmp/andronix-session-$(id -u).log 2>/dev/null | tr "\n" " ")"; fi
vncserver-stop :1 >/dev/null 2>&1; sleep 3'
for r in $(seq 1 "$rounds"); do
	for d in off on; do
		res=$(tx <<T
andronix start $distro --root -- sh -c '$toggle' sh $d >/dev/null 2>&1
andronix start $distro -- sh -c '$measure'
T
)
		log "round $r defaults=$d: $(echo "$res" | tail -1)"
	done
done
tx <<T >/dev/null
andronix start $distro --root -- sh -c '$toggle' sh on
T
log "done (the distro stays installed; andronix remove $distro to clean up)"
