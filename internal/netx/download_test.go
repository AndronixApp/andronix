package netx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fastRetries(t *testing.T) {
	oldS, oldU := stallTimeout, retryUnit
	stallTimeout, retryUnit = 50*time.Millisecond, time.Millisecond
	t.Cleanup(func() { stallTimeout, retryUnit = oldS, oldU })
}

// #286 (#1108, AX-WXCDVP): a link that stalls again and again but moves a
// little each time finishes, resuming each time, instead of failing after a
// fixed number of stalls (the old 8).
func TestDownloadKeepsResumingWhileMoving(t *testing.T) {
	fastRetries(t)
	body := strings.Repeat("andronix", 5) // 40 bytes
	const chunk = 2                       // per attempt, then a stall: 20 attempts
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		from := 0
		if rg := r.Header.Get("Range"); rg != "" {
			from, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
			w.Header().Set("Content-Range", "bytes "+strconv.Itoa(from)+"-"+strconv.Itoa(len(body)-1)+"/"+strconv.Itoa(len(body)))
			w.Header().Set("Content-Length", strconv.Itoa(len(body)-from))
			w.WriteHeader(206)
		} else {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			w.WriteHeader(200)
		}
		end := from + chunk
		if end > len(body) {
			end = len(body)
		}
		w.Write([]byte(body[from:end]))
		w.(http.Flusher).Flush()
		if end < len(body) {
			select { // stall until the client gives up on this attempt
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
		}
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "f.part")
	if _, err := Download(context.Background(), srv.URL, dst, nil, int64(len(body)), nil); err != nil {
		t.Fatalf("download failed after %d requests: %v", reqs.Load(), err)
	}
	if b, _ := os.ReadFile(dst); string(b) != body {
		t.Errorf("got %q", b)
	}
	if n := reqs.Load(); n <= idleAttempts {
		t.Errorf("only %d requests; the test should need more than %d", n, idleAttempts)
	}
}

// A link that never sends anything still gives up after idleAttempts.
func TestDownloadGivesUpWhenIdle(t *testing.T) {
	fastRetries(t)
	var reqs atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		w.Header().Set("Content-Length", "10")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()
	_, err := Download(context.Background(), srv.URL, filepath.Join(t.TempDir(), "f.part"), nil, 10, nil)
	if err == nil || !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("want a stall error, got %v", err)
	}
	if n := reqs.Load(); n != idleAttempts {
		t.Errorf("%d requests, want %d", n, idleAttempts)
	}
}
