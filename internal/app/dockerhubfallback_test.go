package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	clog "github.com/charmbracelet/log"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

// #357: our mirror's HEAD drops (a network blip) and Docker Hub fails too:
// the mirror is tried again instead of failing at "Finding the best download".
func TestDockerHubFailFallsBackToMirror(t *testing.T) {
	oldW, oldH := mirrorRetryWait, headWait
	mirrorRetryWait, headWait = 10*time.Millisecond, 10*time.Millisecond
	t.Cleanup(func() { mirrorRetryWait, headWait = oldW, oldH })

	var drops atomic.Int32
	drops.Store(4) // more than one headRetry's 3 tries: forces the registry, then the second mirror try
	mirror := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			w.Write([]byte(sha + "  x.tar.xz\n"))
			return
		}
		if drops.Add(-1) >= 0 {
			// Drop the connection: a network error, not an HTTP status.
			c, _, _ := w.(http.Hijacker).Hijack()
			c.Close()
			return
		}
		w.Header().Set("Content-Length", "7")
	}))
	defer mirror.Close()
	d, _ := conf.LoadDistro("debian")
	d.Image = "127.0.0.1:1/library/debian:13" // a registry that refuses connections
	lg := &Logger{Logger: clog.New(io.Discard)}
	t.Setenv("ANDRONIX_MIRROR", mirror.URL)
	t.Setenv("ANDRONIX_ROOTFS", "")
	t.Setenv("ANDRONIX_SOURCE", "")
	var said []string
	s, err := pickSource(context.Background(), d, "aarch64", Paths{Cache: t.TempDir()}, lg, func(l string) { said = append(said, l) })
	if err != nil || s.kind != "tarball" || s.sha != sha {
		t.Fatalf("want the mirror tarball after the registry failed, got %+v %v", s, err)
	}
	if len(said) != 1 || !strings.Contains(said[0], "Andronix mirror") {
		t.Errorf("progress lines: %q", said)
	}

	// A single dropped HEAD is retried in place (never reaches the registry).
	drops.Store(1)
	said = nil
	if s, err := pickSource(context.Background(), d, "aarch64", Paths{Cache: t.TempDir()}, lg, func(l string) { said = append(said, l) }); err != nil || s.kind != "tarball" || len(said) != 0 {
		t.Errorf("one drop: %+v %v %q", s, err, said)
	}

	// A mirror 404 is not transient: no second try, the registry error stands.
	nf := httptest.NewServer(http.NotFoundHandler())
	defer nf.Close()
	t.Setenv("ANDRONIX_MIRROR", nf.URL)
	said = nil
	if _, err := pickSource(context.Background(), d, "aarch64", Paths{Cache: t.TempDir()}, lg, func(l string) { said = append(said, l) }); err == nil || len(said) != 0 {
		t.Errorf("404 mirror: err=%v lines=%q", err, said)
	}
}
