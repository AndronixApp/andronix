package app

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/netx"
)

// The resolver tries the next products-api address only when one can't be
// reached: a dead first address falls through, an HTTP answer doesn't.
func TestResolveAPIFallback(t *testing.T) {
	hits := 0
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Write([]byte(`{"version":"2.0.5","url":"https://dl.andronix.app/bin/2.0.5/x","sha256":"` + strings.Repeat("a", 64) + `"}`))
	}))
	defer ok.Close()
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid or expired beta token", http.StatusForbidden)
	}))
	defer refusing.Close()
	dead := "http://127.0.0.1:1" // nothing listens: connection refused

	t.Setenv("ANDRONIX_API", dead+","+ok.URL)
	r, err := resolveQuery(context.Background(), "bin", url.Values{})
	if err != nil || r == nil || r.Version != "2.0.5" || hits != 1 {
		t.Fatalf("dead first address: %v %v (hits %d)", r, err, hits)
	}

	t.Setenv("ANDRONIX_API", refusing.URL+","+ok.URL)
	_, err = resolveQuery(context.Background(), "bin", url.Values{})
	var se *netx.StatusError
	if !errors.As(err, &se) || se.Code != 403 || hits != 1 {
		t.Fatalf("an HTTP answer must not fall through: %v (hits %d)", err, hits)
	}
}
