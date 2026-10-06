//go:build network

package app

import (
	"context"
	"net/url"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/netx"
)

// Real network (go test -tags network, tests/api-hosts.sh): both
// products-api addresses give the same stable answer, and an unreachable
// first address falls through to the real one.
func TestAPIHostsLive(t *testing.T) {
	q := func() url.Values { return url.Values{"flavor": {"android"}} }
	var got []*Resolved
	for _, api := range []string{"https://api.andronix.app", "https://products.andronix.xyz"} {
		t.Setenv("ANDRONIX_API", api)
		r, err := resolveQuery(context.Background(), "bin", q())
		if err != nil {
			t.Fatalf("%s: %v", api, err)
		}
		t.Logf("%s: %s %s %s", api, r.Version, r.SHA256[:12], r.URL)
		got = append(got, r)
	}
	if got[0].Version != got[1].Version || got[0].SHA256 != got[1].SHA256 || got[0].URL != got[1].URL {
		t.Errorf("the two addresses differ: %+v vs %+v", got[0], got[1])
	}
	t.Setenv("ANDRONIX_API", "http://127.0.0.1:1,https://api.andronix.app")
	if r, err := resolveQuery(context.Background(), "bin", q()); err != nil || r.SHA256 != got[0].SHA256 {
		t.Errorf("fallback to api.andronix.app: %v %v", r, err)
	}
	// The beta channel without a token: the same refusal from both.
	codes := []int{}
	for _, api := range []string{"https://api.andronix.app", "https://products.andronix.xyz"} {
		t.Setenv("ANDRONIX_API", api)
		qq := q()
		qq.Set("channel", "beta")
		_, err := resolveQuery(context.Background(), "bin", qq)
		var se *netx.StatusError
		if !errorsAsStatus(err, &se) {
			t.Fatalf("%s beta without a token: want an HTTP refusal, got %v", api, err)
		}
		codes = append(codes, se.Code)
	}
	if codes[0] != codes[1] {
		t.Errorf("beta refusal differs: %v", codes)
	}
}

func errorsAsStatus(err error, se **netx.StatusError) bool {
	e, ok := err.(*netx.StatusError)
	if ok {
		*se = e
	}
	return ok
}
