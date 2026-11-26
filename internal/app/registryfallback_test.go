package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	clog "github.com/charmbracelet/log"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/netx"
	"github.com/AndronixApp/andronix-distros/internal/oci"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

func sumOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// fakeRegistry is an https registry whose blobs answer with code (200 serves
// layer). It counts the token refreshes (GET /v2/).
func fakeRegistry(t *testing.T, code int, layer []byte) (*oci.Image, *atomic.Int32) {
	t.Helper()
	var refreshes atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			refreshes.Add(1)
			return // no Www-Authenticate: an empty bearer
		}
		if code != 200 {
			w.WriteHeader(code)
			return
		}
		w.Write(layer)
	}))
	t.Cleanup(srv.Close)
	old := netx.Client
	netx.Client = srv.Client() // trusts the test certificate; plain http still works
	t.Cleanup(func() { netx.Client = old })
	l := oci.Layer{Digest: "sha256:" + sumOf(layer), Size: int64(len(layer))}
	im := &oci.Image{Ref: oci.Ref{Host: strings.TrimPrefix(srv.URL, "https://"), Repo: "library/debian", Tag: "13"},
		Token: "expired", Layers: []oci.Layer{l}, Total: l.Size}
	return im, &refreshes
}

// fakeMirror serves the debian tarball and its .sha256 (sum, or the body's
// own when sum is ""), and counts the tarball GETs.
func fakeMirror(t *testing.T, body []byte, sum string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	if sum == "" {
		sum = sumOf(body)
	}
	var gets atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case !strings.HasPrefix(r.URL.Path, "/rootfs/debian/13/debian-13-aarch64.tar.xz"):
			http.NotFound(w, r)
		case strings.HasSuffix(r.URL.Path, ".sha256"):
			w.Write([]byte(sum + "  debian-13-aarch64.tar.xz\n"))
		default:
			if r.Method == "GET" {
				gets.Add(1)
			}
			http.ServeContent(w, r, "", time.Time{}, strings.NewReader(string(body)))
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &gets
}

// #394, end to end against local servers: the registry refuses the layer
// (401 after a fresh bearer, 403, 429), the install takes our mirror's
// tarball instead, and the tarball's sha256 is checked.
func TestRegistryRefusedFallsBackToMirror(t *testing.T) {
	d, _ := conf.LoadDistro("debian")
	lg := &Logger{Logger: clog.New(io.Discard)}
	tarball := []byte("the mirror's rootfs tarball")
	for _, code := range []int{401, 403, 429} {
		im, refreshes := fakeRegistry(t, code, []byte("layer"))
		mirror, gets := fakeMirror(t, tarball, "")
		t.Setenv("ANDRONIX_MIRROR", mirror.URL)
		t.Setenv("ANDRONIX_SOURCE", "")
		p := Paths{Cache: t.TempDir()}
		dir := filepath.Join(p.Cache, "layers-debian")

		err := pullLayers(context.Background(), im, dir, lg, func(int64, int64) {})
		var se *netx.StatusError
		if !errors.As(err, &se) || se.Code != code {
			t.Fatalf("%d: pullLayers = %v", code, err)
		}
		if code == 401 && refreshes.Load() != 1 {
			t.Errorf("401: %d bearer refreshes, want 1 before giving up on the registry", refreshes.Load())
		}
		var said []string
		s, ok := mirrorAfterRegistry(context.Background(), err, d, "aarch64", p, lg, func(l string) { said = append(said, l) })
		if !ok || s.kind != "tarball" || s.sha != sumOf(tarball) || !strings.HasPrefix(s.url, mirror.URL) {
			t.Fatalf("%d: want the mirror's tarball with its sha256, got %+v ok=%v", code, s, ok)
		}
		if len(said) != 1 || !strings.Contains(said[0], "Andronix mirror") {
			t.Errorf("%d: progress lines %q", code, said)
		}
		damaged, err := fetchTarball(context.Background(), s, func(int64, int64) {})
		if err != nil || damaged {
			t.Fatalf("%d: fetchTarball damaged=%v err=%v", code, damaged, err)
		}
		if b, _ := os.ReadFile(s.file + ".part"); string(b) != string(tarball) {
			t.Errorf("%d: downloaded %q", code, b)
		}
		if gets.Load() != 1 {
			t.Errorf("%d: %d tarball downloads", code, gets.Load())
		}
	}
}

// The fallback's download is checked like any other: a tarball that doesn't
// match the mirror's sha256 is deleted, never unpacked.
func TestRegistryFallbackChecksTheTarball(t *testing.T) {
	d, _ := conf.LoadDistro("debian")
	lg := &Logger{Logger: clog.New(io.Discard)}
	_, _ = fakeRegistry(t, 403, []byte("layer"))
	mirror, _ := fakeMirror(t, []byte("changed on the way"), sha)
	t.Setenv("ANDRONIX_MIRROR", mirror.URL)
	t.Setenv("ANDRONIX_SOURCE", "")
	p := Paths{Cache: t.TempDir()}
	s, ok := mirrorAfterRegistry(context.Background(), &netx.StatusError{Code: 403}, d, "aarch64", p, lg, nil)
	if !ok || s.sha != sha {
		t.Fatalf("source %+v ok=%v", s, ok)
	}
	damaged, err := fetchTarball(context.Background(), s, func(int64, int64) {})
	if err != nil || !damaged {
		t.Fatalf("damaged=%v err=%v, want a checksum mismatch", damaged, err)
	}
	if _, err := os.Stat(s.file + ".part"); !os.IsNotExist(err) {
		t.Errorf("the damaged download is still there: %v", err)
	}

	// A mirror with the tarball but no checksum for it is never used.
	nosum := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, ".sha256") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte("tarball"))
	}))
	defer nosum.Close()
	t.Setenv("ANDRONIX_MIRROR", nosum.URL)
	if s, ok := mirrorAfterRegistry(context.Background(), &netx.StatusError{Code: 403}, d, "aarch64", p, lg, nil); ok {
		t.Errorf("used a tarball with no checksum: %+v", s)
	}
}

// No fallback when it can't help or wasn't asked for: the registry's own
// error stands, in words.
func TestRegistryFallbackLimits(t *testing.T) {
	d, _ := conf.LoadDistro("debian")
	lg := &Logger{Logger: clog.New(io.Discard)}
	p := Paths{Cache: t.TempDir()}
	mirror, _ := fakeMirror(t, []byte("tarball"), "")
	t.Setenv("ANDRONIX_MIRROR", mirror.URL)
	t.Setenv("ANDRONIX_SOURCE", "")
	bg := context.Background()
	refused := &netx.StatusError{Code: 403}

	// Not a refusal: a dropped connection, a 404, a 5xx resume where they are.
	for _, err := range []error{errors.New("unexpected EOF"), &netx.StatusError{Code: 404}, &netx.StatusError{Code: 503}, errLayerDamaged} {
		if _, ok := mirrorAfterRegistry(bg, err, d, "aarch64", p, lg, nil); ok {
			t.Errorf("fell back on %v", err)
		}
	}
	// Cancelled.
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if _, ok := mirrorAfterRegistry(ctx, refused, d, "aarch64", p, lg, nil); ok {
		t.Error("fell back after a cancel")
	}
	// The registry was asked for by name.
	t.Setenv("ANDRONIX_SOURCE", "registry")
	if _, ok := mirrorAfterRegistry(bg, refused, d, "aarch64", p, lg, nil); ok {
		t.Error("fell back with ANDRONIX_SOURCE=registry")
	}
	t.Setenv("ANDRONIX_SOURCE", "")
	// Our mirror has no tarball for this CPU (x86_64 today): nothing is
	// said about a mirror that then isn't used.
	var said []string
	if _, ok := mirrorAfterRegistry(bg, refused, d, "x86_64", p, lg, func(l string) { said = append(said, l) }); ok || len(said) != 0 {
		t.Errorf("fell back to a tarball the mirror doesn't have (lines %q)", said)
	}

	const hub = "registry-1.docker.io"
	var ue *ui.UserError
	if err := registryDownloadErr(&netx.StatusError{Code: 429}, hub); !errors.As(err, &ue) || ue.Class != "the_image_server_is_busy" {
		t.Errorf("429: %+v", err)
	}
	// Docker Hub's limit isn't blamed for another registry's 429 (Void: ghcr.io).
	if err := registryDownloadErr(&netx.StatusError{Code: 429}, "ghcr.io"); !errors.As(err, &ue) || ue.Class != "download" || ue.Kind != "http_429" {
		t.Errorf("ghcr 429: %+v", err)
	}
	if err := registryDownloadErr(refused, hub); !errors.As(err, &ue) || ue.Class != "download" || ue.Kind != "http_403" {
		t.Errorf("403: %+v", err)
	}
	// #243: never the user's "token refused".
	if err := registryDownloadErr(&netx.StatusError{Code: 401}, hub); !errors.As(err, &ue) || ue.Class != "download" {
		t.Errorf("401: %+v", err)
	}
}

// The download step itself (downloadRegistry, as install.go calls it): the
// registry refuses, the step's source becomes the mirror's tarball, the
// half-fetched layers are gone, the checked tarball is where the unpack step
// looks for it. And without a mirror tarball: the registry's error, the
// source untouched.
func TestDownloadStepRegistryToMirror(t *testing.T) {
	d, _ := conf.LoadDistro("debian")
	lg := &Logger{Logger: clog.New(io.Discard)}
	body := []byte("the mirror's rootfs tarball")
	im, _ := fakeRegistry(t, 403, []byte("layer"))
	mirror, gets := fakeMirror(t, body, "")
	t.Setenv("ANDRONIX_MIRROR", mirror.URL)
	t.Setenv("ANDRONIX_SOURCE", "")
	p := Paths{Cache: t.TempDir()}
	layers := filepath.Join(p.Cache, "layers-debian")
	os.MkdirAll(layers, 0o755)
	os.WriteFile(filepath.Join(layers, "half.part"), []byte("half a layer"), 0o644)

	src := source{kind: "registry", image: im}
	// The step's tarball download, as in install.go.
	tarball := func() error {
		damaged, err := fetchTarball(context.Background(), src, func(int64, int64) {})
		if err != nil || damaged {
			return errors.New("tarball failed")
		}
		return os.Rename(src.file+".part", src.file)
	}
	rep := &lineRep{}
	if via, err := downloadRegistry(context.Background(), &src, d, "aarch64", p, lg, rep, tarball); err != nil || !via {
		t.Fatalf("viaMirror=%v err=%v", via, err)
	}
	if src.kind != "tarball" || src.image != nil || src.sha != sumOf(body) {
		t.Fatalf("the step's source after the fallback: %+v", src)
	}
	if b, _ := os.ReadFile(src.file); string(b) != string(body) {
		t.Errorf("the tarball the unpack step will read: %q", b)
	}
	if _, err := os.Stat(layers); !os.IsNotExist(err) {
		t.Errorf("the registry layers are still on disk: %v", err)
	}
	if len(rep.lines) != 1 || !strings.Contains(rep.lines[0], "using the Andronix mirror") || gets.Load() != 1 {
		t.Errorf("lines %q, %d tarball downloads", rep.lines, gets.Load())
	}

	// No tarball for this CPU: the registry's own error, nothing changed.
	src = source{kind: "registry", image: im}
	called := false
	via, err := downloadRegistry(context.Background(), &src, d, "x86_64", p, lg, &lineRep{}, func() error { called = true; return nil })
	var ue *ui.UserError
	if !errors.As(err, &ue) || ue.Class != "download" || ue.Kind != "http_403" || via || called || src.kind != "registry" {
		t.Errorf("no mirror tarball: err=%+v via=%v tarball called=%v src=%+v", err, via, called, src.kind)
	}

	// A registry that works: its layers, no mirror.
	ok, _ := fakeRegistry(t, 200, []byte("a layer"))
	src = source{kind: "registry", image: ok}
	if via, err := downloadRegistry(context.Background(), &src, d, "aarch64", p, lg, &lineRep{}, func() error { called = true; return nil }); err != nil || via || called || src.kind != "registry" {
		t.Errorf("working registry: err=%v via=%v tarball called=%v src=%s", err, via, called, src.kind)
	}
	if m, _ := filepath.Glob(filepath.Join(layers, "*")); len(m) != 1 {
		t.Errorf("layers on disk: %v", m)
	}
}

// pullLayers as before the fallback: a good layer is kept and not fetched
// twice, a layer that doesn't match its digest is deleted.
func TestPullLayers(t *testing.T) {
	lg := &Logger{Logger: clog.New(io.Discard)}
	layer := []byte("a layer")
	im, _ := fakeRegistry(t, 200, layer)
	dir := filepath.Join(t.TempDir(), "layers")
	var last, total int64
	if err := pullLayers(context.Background(), im, dir, lg, func(c, t int64) { last, total = c, t }); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, strings.TrimPrefix(im.Layers[0].Digest, "sha256:"))
	if b, _ := os.ReadFile(dst); string(b) != string(layer) {
		t.Errorf("layer on disk: %q", b)
	}
	if last != int64(len(layer)) || total != im.Total {
		t.Errorf("progress %d/%d", last, total)
	}
	// Again: nothing to fetch (the server would now be asked for nothing).
	called := false
	if err := pullLayers(context.Background(), im, dir, lg, func(int64, int64) { called = true }); err != nil || called {
		t.Errorf("second pull: err=%v fetched=%v", err, called)
	}

	bad, _ := fakeRegistry(t, 200, layer)
	bad.Layers[0].Digest = "sha256:" + sha
	dir2 := filepath.Join(t.TempDir(), "layers")
	if err := pullLayers(context.Background(), bad, dir2, lg, func(int64, int64) {}); !errors.Is(err, errLayerDamaged) {
		t.Fatalf("want errLayerDamaged, got %v", err)
	}
	if m, _ := filepath.Glob(filepath.Join(dir2, "*")); len(m) != 0 {
		t.Errorf("a damaged layer was left behind: %v", m)
	}
}

// Docker Hub's limit is named only for Docker Hub: another registry's 429 at
// "Finding the best download" is a plain "couldn't reach".
func TestBusyTextOnlyForDockerHub(t *testing.T) {
	d, _ := conf.LoadDistro("void")
	lg := &Logger{Logger: clog.New(io.Discard)}
	im, _ := fakeRegistry(t, 429, []byte("layer"))
	d.Image = im.Ref.Host + "/void-linux/void-glibc:latest"
	t.Setenv("ANDRONIX_SOURCE", "registry")
	t.Setenv("ANDRONIX_ROOTFS", "")
	_, err := pickSource(context.Background(), d, "aarch64", Paths{Cache: t.TempDir()}, lg, nil)
	var ue *ui.UserError
	if !errors.As(err, &ue) || ue.Class == "the_image_server_is_busy" || !strings.Contains(ue.What, "429") {
		t.Errorf("another registry's 429: %+v", err)
	}
}
