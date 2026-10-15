#!/usr/bin/env bash
# libnss_andronix.so.2 (card #141) in real glibc distros, in Docker: with
# the distro's resolv.conf dead, names still resolve through a forwarder
# (andronix __dnsfwd, here on Go's resolver), apt works, a missing name
# returns at once, and a killed forwarder falls back to dns.
#   tests/nss-test.sh [image ...]   (default: ubuntu:24.04 debian:13 fedora:43)
set -uo pipefail
main() {
cd "$(dirname "$0")/.."
local bin=dist/andronix-linux-aarch64 lib=guest/preload/aarch64/libnss_andronix.so.2 rc=0
[ -x "$bin" ] || { echo "build first: NO_NDK=1 ci/build-go.sh"; exit 2; }
for img in "${@:-ubuntu:24.04 debian:13 fedora:43}"; do for i in $img; do
    echo "== $i"
    docker run --rm -i -v "$PWD/$bin":/andronix:ro -v "$PWD/$lib":/nss.so:ro "$i" bash -s <<'X' || rc=1
set -u
fpid() { local d; for d in /proc/[0-9]*; do [ "$(tr '\0' ' ' < $d/cmdline 2>/dev/null)" = "/andronix __dnsfwd / $1 " ] && echo ${d#/proc/}; done; } # the forwarder for session $1
ok() { echo "  ok   $1"; }; bad() { echo "  FAIL $1"; fail=1; }; fail=0
d=$(dirname $(ls /usr/lib/*-linux-gnu/libnss_files.so.2 /usr/lib64/libnss_files.so.2 /usr/lib/libnss_files.so.2 2>/dev/null | head -1))
cp /nss.so $d/libnss_andronix.so.2
sed -i 's/^hosts:.*/hosts: files andronix [NOTFOUND=return] dns/' /etc/nsswitch.conf
grep -q '^hosts: files andronix' /etc/nsswitch.conf || echo 'hosts: files andronix [NOTFOUND=return] dns' >> /etc/nsswitch.conf
echo "  glibc $(ldd --version | head -1 | grep -o '[0-9.]*$'), module in $d"
sleep 600 & sess=$!
p=$(/andronix __dnsfwd / $sess) && [ -e /run/andronix/dns/$p ] && ok "starter printed port $p, announced" || bad "starter: '$p'"
fwd=$(fpid $sess); [ -n "$fwd" ] && [ "$(awk '{print $4}' /proc/$fwd/stat)" = 1 ] && ok "forwarder detached (ppid 1)" || bad "forwarder not detached: '$fwd' $(cat /proc/$fwd/stat 2>/dev/null)"
cp /etc/resolv.conf /tmp/resolv.good; echo "nameserver 192.0.2.1" > /etc/resolv.conf   # dead DNS
getent ahosts dl.andronix.app >/tmp/a 2>&1 && grep -q STREAM /tmp/a && ok "getent ahosts via the module ($(head -1 /tmp/a | cut -d' ' -f1))" || bad "getent ahosts: $(cat /tmp/a)"
getent hosts dl.andronix.app >/dev/null && ok "gethostbyname path" || bad "gethostbyname path"
s=$(date +%s); getent hosts nope.invalid >/dev/null; r=$?; e=$(( $(date +%s) - s ))
[ $r = 2 ] && [ $e -le 3 ] && ok "missing name: not found in ${e}s" || bad "missing name: exit $r after ${e}s"
if command -v apt-get >/dev/null; then apt-get update -qq >/tmp/apt 2>&1 && ok "apt-get update with dead resolv.conf" || bad "apt-get update: $(tail -2 /tmp/apt)"
else dnf -q makecache >/tmp/dnf 2>&1 && ok "dnf makecache with dead resolv.conf" || bad "dnf: $(tail -2 /tmp/dnf)"; fi
if command -v perl >/dev/null; then # a listener that accepts and never answers
  perl -MIO::Socket::INET -e '$s=IO::Socket::INET->new(LocalAddr=>"127.0.0.1",LocalPort=>40999,Listen=>5,ReuseAddr=>1) or die; while($c=$s->accept){push @k,$c}' & bh=$!; sleep 1
  mkdir -p /run/andronix/dns; echo 1 > /run/andronix/dns/40999
  cp /tmp/resolv.good /etc/resolv.conf
  s=$(date +%s%N); getent ahosts deb.debian.org >/dev/null; e=$(( ($(date +%s%N) - s) / 1000000 ))
  [ $e -lt 2500 ] && ok "a silent forwarder costs ${e} ms (both files asked, then dns)" || bad "silent forwarder: ${e} ms"
  rm -f /run/andronix/dns/40999; kill $bh; echo "nameserver 192.0.2.1" > /etc/resolv.conf
fi
for sp in 1 2 3 4; do echo 1 > /run/andronix/dns/$sp; done   # stale files (nothing listens; pid 1 lives but isn't a forwarder)
echo "nameserver 192.0.2.1" > /etc/resolv.conf; s=$(date +%s%N)
getent ahosts example.org >/dev/null; r=$?; e=$(( ($(date +%s%N) - s) / 1000000 ))
[ $r = 0 ] && ok "4 stale files don't hide the forwarder (${e} ms)" || bad "4 stale files hid the forwarder (exit $r)"
sleep 30 & s4=$!; /andronix __dnsfwd / $s4 >/dev/null
[ -z "$(ls /run/andronix/dns | grep -x '[1-4]')" ] && ok "a new forwarder removed files naming a live non-forwarder pid" || bad "stale files kept: $(ls /run/andronix/dns | tr '\n' ' ')"
kill $s4
kill $fwd; wait $fwd 2>/dev/null; sleep 1
[ -n "$(ls /run/andronix/dns 2>/dev/null)" ] && echo "  (stale port file left, as after a crash)"
cp /tmp/resolv.good /etc/resolv.conf
s=$(date +%s); getent ahosts dl.andronix.app >/dev/null && ok "forwarder killed: dns fallback in $(( $(date +%s) - s ))s" || bad "forwarder killed: no fallback"
sleep 30 & sess2=$!; /andronix __dnsfwd / $sess2 >/dev/null
[ ! -e /run/andronix/dns/$(ls /run/andronix/dns | head -1) ] || [ "$(ls /run/andronix/dns | wc -l)" = 1 ] && ok "a new forwarder cleaned the stale file" || bad "stale file kept: $(ls /run/andronix/dns)"
kill $sess2; sleep 2; [ -z "$(ls /run/andronix/dns)" ] && ok "forwarder left with its session" || bad "forwarder outlived its session"
sleep 60 & s3=$!; /andronix __dnsfwd / $s3 >/dev/null; kill -9 $s3
for i in $(seq 1 10); do [ -n "$(fpid $s3)" ] || break; sleep 1; done
[ -n "$(fpid $s3)" ] && bad "kill -9 of the session: forwarder still there after 10 s" || ok "kill -9 of the session: forwarder gone in ${i}s"
if command -v perl >/dev/null; then # a session that ends but is never reaped (zombie)
  perl -e '$|=1; if(!($c=fork)){exit 0} print "$c\n"; sleep 30' > /tmp/z & zp=$!; sleep 1; z=$(cat /tmp/z)
  /andronix __dnsfwd / $z >/dev/null 2>&1; sleep 3
  [ -n "$(fpid $z)" ] && bad "zombie session: forwarder still there" || ok "zombie session: forwarder gone"
  kill $zp
fi
kill $sess 2>/dev/null
exit $fail
X
done; done
exit $rc
}
main "$@"
