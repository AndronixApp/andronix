package app

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	charmlog "github.com/charmbracelet/log"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/klauspost/compress/zstd"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/rootfs"
)

func testLog() *Logger { return &Logger{Logger: charmlog.New(io.Discard)} }

// testImage is a tiny rootfs as a tar.zst: etc/passwd and the XFCE session.
func testImage(t *testing.T) ([]byte, string) {
	t.Helper()
	var tb bytes.Buffer
	tw := tar.NewWriter(&tb)
	for _, f := range []struct{ name, body string }{
		{"./etc/passwd", "root:x:0:0:root:/root:/bin/bash\n"},
		{"./usr/bin/startxfce4", "#!/bin/sh\n" + strings.Repeat("# padding\n", 20000)},
	} {
		tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o755, Size: int64(len(f.body)), Typeflag: tar.TypeReg})
		tw.Write([]byte(f.body))
	}
	tw.Close()
	var zb bytes.Buffer
	zw, _ := zstd.NewWriter(&zb)
	zw.Write(tb.Bytes())
	zw.Close()
	h := sha256.Sum256(zb.Bytes())
	return zb.Bytes(), hex.EncodeToString(h[:])
}

func xfce(t *testing.T) (*conf.Distro, *conf.Desktop) {
	d, err := conf.LoadDistro("debian")
	if err != nil {
		t.Fatal(err)
	}
	de, _ := conf.ResolveDesktop("xfce")
	return d, de
}

func TestImageEligible(t *testing.T) {
	_, de := xfce(t)
	none, _ := conf.ResolveDesktop("none")
	t.Setenv("ANDRONIX_ROOTFS", "")
	for _, c := range []struct {
		name    string
		o       InstallOpts
		edition string
		de      *conf.Desktop
		want    bool
	}{
		{"free xfce", InstallOpts{}, "", de, true},
		{"modded", InstallOpts{Modded: true}, "debian-xfce", de, false},
		{"edition (Classic)", InstallOpts{}, "legacy-debian", de, false},
		{"command line only", InstallOpts{}, "", none, false},
		{"building an image", InstallOpts{ImageBuild: true}, "", de, false},
	} {
		if got := imageEligible(c.o, c.edition, c.de); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	t.Setenv("ANDRONIX_ROOTFS", "/some/base.tar.xz")
	if imageEligible(InstallOpts{}, "", de) {
		t.Error("ANDRONIX_ROOTFS set: want the base, not an image")
	}
}

// mirror serves one image under its real path; mode changes what it does.
func mirror(t *testing.T, body []byte, sum string, mode *string, gets *int32) *httptest.Server {
	t.Helper()
	d, de := xfce(t)
	dir := "/rootfs/debian/" + d.Version + "/"
	base := strings.TrimSuffix(imageName(d, de, "aarch64"), ".tar.zst")
	path := dir + base + "-20261006.tar.zst" // the dated image the pointer names
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case dir + base + ".json":
			switch *mode {
			case "missing":
				http.NotFound(w, r)
			case "nosum":
				io.WriteString(w, `{"url":"`+strings.TrimPrefix(path, "/")+`","size":`+strconv.Itoa(len(body))+`}`)
			default:
				io.WriteString(w, `{"url":"`+strings.TrimPrefix(path, "/")+`","sha256":"`+sum+`","size":`+strconv.Itoa(len(body))+`,"unpacked":123456,"built":"2026-10-06"}`)
			}
		case path:
			n := atomic.AddInt32(gets, 1)
			from := 0
			if rg := r.Header.Get("Range"); rg != "" {
				from, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(rg, "bytes="), "-"))
				w.Header().Set("Content-Range", "bytes "+strconv.Itoa(from)+"-"+strconv.Itoa(len(body)-1)+"/"+strconv.Itoa(len(body)))
				w.Header().Set("Content-Length", strconv.Itoa(len(body)-from))
				w.WriteHeader(206)
			} else {
				w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			}
			b := body[from:]
			if *mode == "drop" && n == 1 {
				// The connection drops half way: the stream resumes by Range.
				w.Write(b[:len(b)/2])
				if hj, ok := w.(http.Hijacker); ok {
					c, _, _ := hj.Hijack()
					c.Close()
				}
				return
			}
			if *mode == "corrupt" {
				b = append([]byte{}, b...)
				b[len(b)-10] ^= 0xff
			}
			w.Write(b)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestFindImage(t *testing.T) {
	d, de := xfce(t)
	body, sum := testImage(t)
	mode := ""
	var gets int32
	srv := mirror(t, body, sum, &mode, &gets)
	defer srv.Close()
	t.Setenv("ANDRONIX_MIRROR", srv.URL)
	t.Setenv("ANDRONIX_IMAGE", "")
	t.Setenv("ANDRONIX_NO_IMAGE", "")
	ctx := context.Background()

	s, reason := findImage(ctx, d, de, "aarch64", testLog())
	if reason != "" || s.kind != "image" || s.sha != sum || s.unpacked != 123456 || s.size != int64(len(body)) {
		t.Fatalf("published image: %+v %q", s, reason)
	}
	for m, want := range map[string]string{"missing": fbNoImage, "nosum": fbNoSum} {
		mode = m
		if _, reason := findImage(ctx, d, de, "aarch64", testLog()); reason != want {
			t.Errorf("%s: reason %q, want %q", m, reason, want)
		}
	}
	mode = ""
	t.Setenv("ANDRONIX_NO_IMAGE", "1")
	if _, reason := findImage(ctx, d, de, "aarch64", testLog()); reason != fbOff {
		t.Errorf("ANDRONIX_NO_IMAGE: %q", reason)
	}
	t.Setenv("ANDRONIX_NO_IMAGE", "")
	srv.Close()
	if _, reason := findImage(ctx, d, de, "aarch64", testLog()); reason != fbOffline {
		t.Errorf("mirror down: %q, want %q", reason, fbOffline)
	}

	// ANDRONIX_IMAGE: a local file with its .sha256 (and .meta).
	dir := t.TempDir()
	f := filepath.Join(dir, "debian-13-xfce-aarch64.tar.zst")
	os.WriteFile(f, body, 0o644)
	t.Setenv("ANDRONIX_IMAGE", f)
	if _, reason := findImage(ctx, d, de, "aarch64", testLog()); reason != fbNoSum {
		t.Errorf("local image without .sha256: %q", reason)
	}
	os.WriteFile(f+".sha256", []byte(sum+"  x\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "debian-13-xfce-aarch64.meta"), []byte("unpacked=99\n"), 0o644)
	if s, reason := findImage(ctx, d, de, "aarch64", testLog()); reason != "" || s.file != f || s.unpacked != 99 {
		t.Errorf("local image: %+v %q", s, reason)
	}
}

func TestUnpackImage(t *testing.T) {
	d, de := xfce(t)
	body, sum := testImage(t)
	ctx := context.Background()
	for _, c := range []struct{ mode, reason string }{
		{"", ""},
		{"drop", ""}, // resumed by Range
		{"corrupt", fbChecksum},
	} {
		mode := c.mode
		var gets int32
		srv := mirror(t, body, sum, &mode, &gets)
		t.Setenv("ANDRONIX_MIRROR", srv.URL)
		t.Setenv("ANDRONIX_IMAGE", "")
		s, reason := findImage(ctx, d, de, "aarch64", testLog())
		if reason != "" {
			t.Fatalf("%s: find: %q", c.mode, reason)
		}
		root := t.TempDir()
		x := &rootfs.Extractor{Root: root}
		n, reason, err := unpackImage(ctx, s, x, nil)
		srv.Close()
		if reason != c.reason {
			t.Errorf("%q: reason %q (%v), want %q", c.mode, reason, err, c.reason)
			continue
		}
		if c.reason != "" {
			continue
		}
		if n != int64(len(body)) {
			t.Errorf("%q: %d bytes downloaded, want %d", c.mode, n, len(body))
		}
		if _, err := os.Stat(filepath.Join(root, "usr/bin/startxfce4")); err != nil {
			t.Errorf("%q: not unpacked: %v", c.mode, err)
		}
		if c.mode == "drop" && gets < 2 {
			t.Errorf("drop: %d requests, want a resume", gets)
		}
	}

	// Not an archive at all, but its checksum matches: a bad image (unpack),
	// not a damaged download.
	dir := t.TempDir()
	f := filepath.Join(dir, "x.tar.zst")
	garbage := []byte("not a tarball")
	os.WriteFile(f, garbage, 0o644)
	gs := sha256.Sum256(garbage)
	_, reason, _ := unpackImage(ctx, source{kind: "image", file: f, sha: hex.EncodeToString(gs[:])}, &rootfs.Extractor{Root: t.TempDir()}, nil)
	if reason != fbUnpack {
		t.Errorf("garbage: reason %q, want %q", reason, fbUnpack)
	}
}

// PREBUILT=image skips only the desktop packages; prebuilt() (Modded's
// skip-everything) is unchanged and stays false for it.
func TestImageInstalled(t *testing.T) {
	_, de := xfce(t)
	d := t.TempDir()
	in := &Inst{Dir: d, Rootfs: filepath.Join(d, "rootfs"), State: filepath.Join(d, "install.conf")}
	os.MkdirAll(filepath.Join(in.Rootfs, "usr/bin"), 0o755)
	os.WriteFile(filepath.Join(in.Rootfs, "usr/bin/startxfce4"), nil, 0o755)
	in.Set("PREBUILT", "image")
	if !imageInstalled(in, de) {
		t.Error("PREBUILT=image with the session: want image-installed")
	}
	if prebuilt(in, de) {
		t.Error("PREBUILT=image must not count as prebuilt (that skips refresh and update)")
	}
	in.Set("PREBUILT", "yes")
	if imageInstalled(in, de) {
		t.Error("PREBUILT=yes is Modded's, not an image")
	}
	in.Set("PREBUILT", "image")
	os.Remove(filepath.Join(in.Rootfs, "usr/bin/startxfce4"))
	if imageInstalled(in, de) {
		t.Error("no session binary: want the package steps")
	}
}

func TestParseImagePointer(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	s, ok := parseImagePointer([]byte(`{"url":"rootfs/debian/13/x-20261006.tar.zst","sha256":"`+sum+`","size":5,"unpacked":9}`), "https://dl.example/")
	if !ok || s.url != "https://dl.example/rootfs/debian/13/x-20261006.tar.zst" || s.size != 5 || s.unpacked != 9 || s.sha != sum {
		t.Errorf("relative url: %+v %v", s, ok)
	}
	if s, ok := parseImagePointer([]byte(`{"url":"https://dl.example/rootfs/a.tar.zst","sha256":"`+sum+`"}`), "https://dl.example"); !ok || s.url != "https://dl.example/rootfs/a.tar.zst" {
		t.Errorf("absolute url on the mirror's host: %+v %v", s, ok)
	}
	for _, u := range []string{"https://cdn.example/a.tar.zst", "http://dl.example/rootfs/a.tar.zst", "ftp://dl.example/a"} {
		if _, ok := parseImagePointer([]byte(`{"url":"`+u+`","sha256":"`+sum+`"}`), "https://dl.example"); ok {
			t.Errorf("accepted %s (another host or not https)", u)
		}
	}
	for _, bad := range []string{
		`{"url":"rootfs/a.tar.zst"}`,
		`{"url":"rootfs/a.tar.zst","sha256":"nothex"}`,
		`{"sha256":"` + sum + `"}`,
		`{"url":"../../etc/x","sha256":"` + sum + `"}`,
		`not json`,
	} {
		if _, ok := parseImagePointer([]byte(bad), "https://dl.example"); ok {
			t.Errorf("accepted %s", bad)
		}
	}
}
