#!/bin/sh
# Real network: both products-api addresses answer the same (rc check).
#   tests/api-hosts.sh
# 1. /ready and the stable resolver answer from each address, compared.
# 2. get.sh's own lookup loop: a dead first address falls through.
# 3. The installer's code paths (go test -tags network).
set -u
cd "$(dirname "$0")/.."
fail=0
q='item=bin&arch=aarch64&flavor=android&v=rc-check'
for api in https://api.andronix.app https://products.andronix.xyz; do
    r=$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$api/ready")
    j=$(curl -fsS -m 10 "$api/v1/installer/resolve?$q") || { echo "FAIL $api resolve"; fail=1; continue; }
    echo "$api  ready=$r  $(printf %s "$j" | tr -d '\n' | cut -c1-160)"
    eval "ans_$(printf %s "$api" | tr -c 'a-z' _)=\$j"
    [ "$r" = 200 ] || { echo "FAIL $api /ready $r"; fail=1; }
done
[ "${ans_https___api_andronix_app:-x}" = "${ans_https___products_andronix_xyz:-y}" ] && echo "same answer: ok" || { echo "FAIL: the answers differ"; fail=1; }
# get.sh's loop, as written there.
j=$(sh -c 'curl_works(){ true; }; wget_works(){ false; }; q="'"$q"'"; ANDRONIX_API="http://127.0.0.1:1,https://api.andronix.app"; eval "$(sed -n "/^        j=\"\"\$/,/^        done\$/p" get.sh)"; printf %s "$j"')
[ -n "$j" ] && [ "$j" = "${ans_https___api_andronix_app:-}" ] && echo "get.sh fallback: ok" || { echo "FAIL get.sh fallback: [$j]"; fail=1; }
go test -count=1 -tags network -run TestAPIHostsLive -v ./internal/app 2>&1 | grep -E "^(---|ok|FAIL)|apilive_test" || fail=1
[ $fail = 0 ] && echo "api-hosts: PASS" || { echo "api-hosts: FAIL"; exit 1; }
