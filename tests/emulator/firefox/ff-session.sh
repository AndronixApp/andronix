#!/bin/sh
# Inside a distro (andronix start <distro> -- sh /tmp/ff-session.sh MINUTES):
# a long Firefox session on real sites, the way XFCE's 'Web Browser' does
# it: one Firefox, and every 30 s the next URL handed to it with
# 'firefox --new-tab' (the remote path exo-open uses). A parent that stops
# answering shows as a --new-tab call that doesn't return (HANG), and its
# CPU is sampled (parent_cpu: % of one core over 5 s). Draws into a private Xvnc. Owner's bug, Oct 6: the parent
# spun at ~60% CPU and no site loaded any more.
#   FF_PREFS   extra user.js lines (e.g. dom.ipc.forkserver.enable false)
#   FF_DISPLAY draw on this display (a running desktop) instead of a private Xvnc
#   FF_KILL    content|forkserver|both: SIGKILL that at tab FF_KILL_AT (default 4)
#   FF_IDLE    seconds of idle sampling after the tabs
#   FF_UNSET   variables to unset first (e.g. MOZ_DISABLE_CONTENT_SANDBOX)
# Prints RESULT lines; the last is 'RESULT session ok' or 'RESULT session hang'.
min=${1:-20}
# Every line also goes to /tmp/ff-session.result: if Android kills the
# session (phantom process killer), the numbers up to then are still there.
if [ -z "${FF_TEED:-}" ]; then
	FF_TEED=1 sh "$0" "$@" 2>&1 | tee /tmp/ff-session.result
	exit 0
fi
. /etc/profile.d/andronix.sh 2>/dev/null   # the installer's Firefox switches
for v in ${FF_UNSET:-}; do unset "$v"; done
FF=$(command -v firefox-esr || command -v firefox)
[ -n "$FF" ] || { echo "RESULT no-browser"; exit 0; }
P=$(mktemp -d)
[ -n "${FF_PREFS:-}" ] && printf '%s\n' "$FF_PREFS" >"$P/user.js"
echo "RESULT browser=$($FF --version 2>/dev/null | tr ' ' '_') prefs=$(tr '\n' ' ' <"$P/user.js" 2>/dev/null)"
if [ -n "${FF_DISPLAY:-}" ]; then   # the running desktop's display (e.g. :1), so its processes count too
	x=
	export DISPLAY=$FF_DISPLAY
else
	Xvnc :18 -geometry 1280x720 -depth 24 -SecurityTypes None -localhost >/tmp/ff-session-x.log 2>&1 &
	x=$!
	sleep 3
	export DISPLAY=:18
fi
# A session bus, as in the desktop (Firefox's remote may go over D-Bus).
command -v dbus-launch >/dev/null 2>&1 && eval "$(dbus-launch --sh-syntax)"
"$FF" --profile "$P" about:blank >/tmp/ff-session.log 2>&1 &
sleep 20
parent=$(pgrep -o -f -- "--profile $P")
echo "RESULT parent=$parent"
urls="https://www.youtube.com/ https://www.youtube.com/watch?v=aqz-KE-bpKQ https://www.otto.de/ https://en.wikipedia.org/wiki/Linux https://github.com/termux/termux-app https://news.ycombinator.com/ https://www.bbc.com/news https://www.reddit.com/r/linux/"
# The parent's CPU right now (5 s of /proc ticks, % of one core), not ps's lifetime average.
cpu_now() {
	a=$(awk '{print $14+$15}' "/proc/$parent/stat" 2>/dev/null) || return
	sleep 5
	b=$(awk '{print $14+$15}' "/proc/$parent/stat" 2>/dev/null) || return
	echo $(( (b - a) * 100 / (5 * $(getconf CLK_TCK)) ))
}
# FF_KILL=content|forkserver|both at tab FF_KILL_AT (default 4): SIGKILL, the way
# Android's phantom killer ends a process. Does the parent survive it?
partial_kill() {
	case ${FF_KILL:-} in content|both) k=$(pgrep -n -f -- '-contentproc.*tab'); [ -n "$k" ] && kill -9 "$k" && echo "RESULT killed content $k" ;; esac
	case ${FF_KILL:-} in forkserver|both) k=$(pgrep -x forkserver); [ -n "$k" ] && kill -9 $k && echo "RESULT killed forkserver $k" ;; esac
}
end=$(( $(date +%s) + min * 60 ))
n=0 hang=0 rerr=0
while [ "$(date +%s)" -lt "$end" ]; do
	for u in $urls; do
		[ "$(date +%s)" -lt "$end" ] || break
		n=$((n + 1))
		[ -n "${FF_KILL:-}" ] && [ "$n" = "${FF_KILL_AT:-4}" ] && partial_kill
		timeout 25 "$FF" --profile "$P" --new-tab "$u" >/dev/null 2>&1
		case $? in
		0) ;;
		124|137) hang=$((hang + 1)) ;;   # the running Firefox didn't take it in 25 s
		*) rerr=$((rerr + 1)) ;;         # the remote call failed (not a hang)
		esac
		sleep 30
		cpu=$(cpu_now)
		st=$(awk '{print $3}' "/proc/$parent/stat" 2>/dev/null)
		procs=$(pgrep -c -f -- '-contentproc' 2>/dev/null)
		ffall=$(pgrep -c -f -- "$FF" 2>/dev/null)
		all=$(ps -e -o pid= 2>/dev/null | wc -l)   # every process of this app (the phantom killer counts these)
		[ "$all" -gt "${maxall:-0}" ] && maxall=$all
		[ "$ffall" -gt "${maxff:-0}" ] && maxff=$ffall
		[ "$all" -ge "${maxall:-0}" ] && ps -e -o comm= 2>/dev/null | sort | uniq -c | sort -rn | tr -s ' ' | tr '\n' ',' >/tmp/ff-session.procs
		echo "RESULT tab=$n url=$u parent_cpu=${cpu:-gone} state=${st:-gone} contentprocs=$procs firefox_procs=$ffall app_procs=$all remote_timeouts=$hang remote_errors=$rerr"
		[ -d "/proc/$parent" ] || { echo "RESULT parent-gone"; break 2; }
	done
done
# FF_IDLE seconds with no new tabs: an idle parent should drop to near 0 % (owner's bug: it spun).
i=0
while [ "$i" -lt "${FF_IDLE:-0}" ] && [ -d "/proc/$parent" ]; do
	echo "RESULT idle t=${i}s parent_cpu_now=$(cpu_now) contentprocs=$(pgrep -c -f -- '-contentproc')"
	sleep 25; i=$((i + 30))
done
[ "$hang" -gt 2 ] && echo "RESULT session hang ($hang of $n --new-tab calls didn't return)" || echo "RESULT session ok ($n tabs, $hang slow calls, $rerr remote errors)"
grep -a -c 'not safe to map' /tmp/ff-session.log | sed 's/^/RESULT shm_crashes=/'
echo "RESULT max firefox_procs=${maxff:-?} app_procs=${maxall:-?} content_crashes=$(grep -a -c 'exited on signal' /tmp/ff-session.log)"
pkill -f -- "--profile $P"; sleep 2; pkill -9 -f -- "--profile $P"
[ -n "$x" ] && kill "$x" 2>/dev/null
[ -n "${DBUS_SESSION_BUS_PID:-}" ] && kill "$DBUS_SESSION_BUS_PID" 2>/dev/null
rm -rf "$P"
