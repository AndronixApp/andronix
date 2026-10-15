#!/usr/bin/env bash
# libandronix-creat.so (support #326) in Fedora x86_64, in Docker: dnf's
# key import (librepo) calls creat(); with the shim preloaded as the compat
# rule does (/etc/ld.so.preload), every creat binds to the shim (it becomes
# open(O_CREAT|O_WRONLY|O_TRUNC), i.e. openat) and a package install that
# imports the Fedora key works. Docker can't block the creat syscall itself
# here (x86_64 runs emulated); the binding is what proves the redirect.
#   tests/creat-shim-test.sh
set -uo pipefail
cd "$(dirname "$0")/.."
lib=guest/preload/x86_64/libandronix-creat.so
[ -f "$lib" ] || { echo "build first: ci/build-preload.sh"; exit 2; }
docker run --rm --platform linux/amd64 -i -v "$PWD/$lib":/shim.so:ro fedora:44 bash -s <<'X'
ok() { echo "  ok   $1"; }; bad() { echo "  FAIL $1"; fail=1; }; fail=0
mkdir -p /usr/local/lib/andronix && cp /shim.so /usr/local/lib/andronix/libandronix-creat.so
echo /usr/local/lib/andronix/libandronix-creat.so > /etc/ld.so.preload
rm -rf /var/lib/rpm-state/* 2>/dev/null
LD_DEBUG=bindings dnf -y install which >/tmp/o 2>/tmp/b; r=$?
[ $r = 0 ] && ok "dnf install with the shim preloaded (exit 0)" || bad "dnf install: exit $r $(tail -2 /tmp/o)"
n=$(grep -c "normal symbol \`creat\(64\)\?'" /tmp/b); s=$(grep "normal symbol \`creat\(64\)\?'" /tmp/b | grep -c 'libandronix-creat.so')
[ "$n" -gt 0 ] && [ "$n" = "$s" ] && ok "creat bound to the shim ($s of $n bindings)" || bad "creat bindings: $s of $n to the shim"
grep -i "not implemented\|Failed to import" /tmp/o && bad "key import failed" || ok "key import clean"
exit $fail
X
