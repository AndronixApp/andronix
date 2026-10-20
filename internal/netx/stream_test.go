package netx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A resume whose object changed under us must not splice the new object's
// bytes onto the old partial: If-Range makes the server answer 200 (full
// body) and Stream reports ErrSourceChanged so the caller restarts (#243).
func TestStreamIfRangeSourceChanged(t *testing.T) {
	var ifRange string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			// First fetch: advertise 20 bytes but send 10 then drop, forcing
			// a resume.
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Content-Length", "20")
			w.WriteHeader(200)
			w.Write([]byte("0123456789"))
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
			return
		}
		// Resume: remember the If-Range the client sent, then answer as a
		// changed object would — 200 with the whole (new) body.
		ifRange = r.Header.Get("If-Range")
		w.Header().Set("ETag", `"v2"`)
		w.WriteHeader(200)
		w.Write([]byte("brand-new-object-body"))
	}))
	defer srv.Close()

	s, err := OpenStream(context.Background(), srv.URL, 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	_, err = io.ReadAll(s)
	if !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("read error = %v, want ErrSourceChanged", err)
	}
	if ifRange != `"v1"` {
		t.Errorf("If-Range on resume = %q, want the first ETag \"v1\"", ifRange)
	}
}

// A clean resume (unchanged object, 206) splices onto the partial and the
// whole body comes through.
func TestStreamResumeOK(t *testing.T) {
	const body = "0123456789abcdefghij" // 20 bytes
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"v1"`)
		if rng := r.Header.Get("Range"); rng != "" {
			// bytes=10- : honour it (same object), 206 with the rest.
			w.Header().Set("Content-Range", "bytes 10-19/20")
			w.WriteHeader(206)
			w.Write([]byte(body[10:]))
			return
		}
		w.Header().Set("Content-Length", "20")
		w.WriteHeader(200)
		w.Write([]byte(body[:10]))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	}))
	defer srv.Close()

	s, err := OpenStream(context.Background(), srv.URL, 20, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, err := io.ReadAll(s)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != body {
		t.Errorf("body = %q, want %q", got, body)
	}
}
