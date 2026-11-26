package netx

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPeek(t *testing.T) {
	body := strings.Repeat("0123456789", 1000)
	var ranges []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ranges = append(ranges, r.Header.Get("Range"))
		switch r.URL.Path {
		case "/range":
			http.ServeContent(w, r, "", time.Time{}, strings.NewReader(body))
		case "/norange": // a server that ignores Range sends everything
			w.Write([]byte(body))
		case "/short":
			w.Write([]byte("abc"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	ctx := context.Background()
	for _, p := range []string{"/range", "/norange"} {
		b, err := Peek(ctx, srv.URL+p, 16)
		if err != nil || string(b) != "0123456789012345" {
			t.Errorf("%s: %q %v", p, b, err)
		}
	}
	if ranges[0] != "bytes=0-15" {
		t.Errorf("Range sent: %q", ranges[0])
	}
	if b, err := Peek(ctx, srv.URL+"/short", 16); err != nil || string(b) != "abc" {
		t.Errorf("short: %q %v", b, err)
	}
	var se *StatusError
	if _, err := Peek(ctx, srv.URL+"/missing", 16); !errors.As(err, &se) || se.Code != 404 {
		t.Errorf("404: %v", err)
	}
	// One try, no retries: a dead server fails at once.
	srv.Close()
	start := time.Now()
	if _, err := Peek(ctx, srv.URL+"/range", 16); err == nil || time.Since(start) > 2*time.Second {
		t.Errorf("dead server: err=%v after %s", err, time.Since(start))
	}
}
