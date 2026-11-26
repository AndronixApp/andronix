package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// The other tests switch between the real mirrors' URLs without a network:
// no objection from the probe unless a test puts the real one back.
func init() { mirrorProbe = func(context.Context, string, string, string) string { return "" } }

// realProbe puts aptMirrorProblem back for one test.
func realProbe(t *testing.T) {
	t.Helper()
	old := mirrorProbe
	mirrorProbe = aptMirrorProblem
	t.Cleanup(func() { mirrorProbe = old })
}

// aptMirror is one mirror on the fake server: when its Release files were
// made, and what it lacks.
type aptMirror struct {
	date    time.Time
	noSuite string // this suite has no Release file
	noArch  bool   // no package index for this CPU
	signIn  bool   // answers every request with a web page
}

// fakeArchive serves /<name>/dists/<suite>/InRelease and
// /<name>/dists/<suite>/main/binary-<arch>/Release for each mirror.
func fakeArchive(t *testing.T, mirrors map[string]*aptMirror) *httptest.Server {
	t.Helper()
	arch := debArch(sys.DetectArch())
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/"), "/")
		m := mirrors[parts[0]]
		if m == nil || len(parts) < 4 || parts[1] != "dists" {
			http.NotFound(w, r)
			return
		}
		suite, rest := parts[2], strings.Join(parts[3:], "/")
		switch {
		case m.signIn:
			w.Write([]byte("<html><title>Sign in to Wi-Fi</title></html>"))
		case suite == m.noSuite:
			http.NotFound(w, r)
		case rest == "InRelease":
			body := "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\nOrigin: Ubuntu\nSuite: " + suite + "\nDate: " +
				m.date.Format("Mon, 02 Jan 2006 15:04:05 UTC") + "\nArchitectures: arm64 armhf\n" + strings.Repeat("x", 8192)
			http.ServeContent(w, r, "", time.Time{}, strings.NewReader(body))
		case rest == "main/binary-"+arch+"/Release" && !m.noArch:
			w.Write([]byte("Archive: " + suite + "\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ubuntuRoot is a rootfs whose sources (as in our Ubuntu 24.04 image) use
// base; listsFrom, when not zero, is the date of the lists apt has for it.
func ubuntuRoot(t *testing.T, base string, listsFrom time.Time) string {
	t.Helper()
	root := t.TempDir()
	src := filepath.Join(root, "etc/apt/sources.list.d/ubuntu.sources")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.WriteFile(src, []byte("Types: deb\nURIs: "+base+"\nSuites: noble noble-updates noble-backports\nComponents: main universe restricted multiverse\n\n"+
		"Types: deb\nURIs: "+base+"\nSuites: noble-security\nComponents: main universe restricted multiverse\n"), 0o644)
	if !listsFrom.IsZero() {
		name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(base, "http://"), "/"), "/", "_")
		lists := filepath.Join(root, "var/lib/apt/lists")
		os.MkdirAll(lists, 0o755)
		for _, s := range []string{"noble", "noble-updates", "noble-backports", "noble-security"} {
			os.WriteFile(filepath.Join(lists, name+"_dists_"+s+"_InRelease"),
				[]byte("Origin: Ubuntu\nDate: "+listsFrom.Format("Mon, 02 Jan 2006 15:04:05 UTC")+"\n"), 0o644)
		}
	}
	return root
}

func sourcesOf(root string) string {
	b, _ := os.ReadFile(filepath.Join(root, "etc/apt/sources.list.d/ubuntu.sources"))
	return string(b)
}

// #399, support #5242 as it was on Nov 10 2026: the lists on the phone are
// today's, from ports.ubuntu.com; the first fallback stopped syncing 2.4 days
// ago (58 hours). It is passed over for the next mirror, which is a few hours behind.
func TestStaleMirrorIsPassedOver(t *testing.T) {
	realProbe(t)
	now := time.Date(2026, 11, 10, 16, 40, 0, 0, time.UTC)
	mirrors := map[string]*aptMirror{
		"stale": {date: now.Add(-58 * time.Hour)},
		"fresh": {date: now.Add(-12 * time.Hour)},
	}
	srv := fakeArchive(t, mirrors)
	primary, stale, fresh := srv.URL+"/primary/", srv.URL+"/stale/", srv.URL+"/fresh/"
	root := ubuntuRoot(t, primary, now)

	if why := aptMirrorProblem(context.Background(), root, primary, stale); !strings.Contains(why, "out of date") {
		t.Errorf("stale mirror: %q", why)
	}
	if why := aptMirrorProblem(context.Background(), root, primary, fresh); why != "" {
		t.Errorf("fresh mirror: %q", why)
	}

	fam, _ := pkgmgr.Get("apt")
	lg := NewLog("test")
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, fam, nil, lg)
	p.mirrors, p.cur, p.home = [][]string{{primary, stale, fresh}}, []int{0}, []int{0}
	if !p.nextMirror(context.Background(), nopRep{}) {
		t.Fatal("no switch")
	}
	lg.Close()
	if s := sourcesOf(root); strings.Contains(s, stale) || strings.Count(s, fresh) != 2 || p.cur[0] != 2 {
		t.Errorf("want both stanzas on the fresh mirror, cur=%v:\n%s", p.cur, s)
	}
	if b, _ := os.ReadFile(lg.Path); !strings.Contains(string(b), "not switching to "+stale) || !strings.Contains(string(b), "out of date") {
		t.Errorf("the log doesn't say why the stale mirror was passed over:\n%s", b)
	}

	// With no lists on the phone yet (the distro's own server never
	// answered), there is nothing a mirror could be behind: the first one
	// that answers is used.
	root = ubuntuRoot(t, primary, time.Time{})
	if why := aptMirrorProblem(context.Background(), root, primary, stale); why != "" {
		t.Errorf("no lists on disk: %q", why)
	}
	// Up to two days behind is a mirror between syncs; more is one that stopped.
	for lag, usable := range map[time.Duration]bool{0: true, 23 * time.Hour: true, 47 * time.Hour: true, 49 * time.Hour: false, -3 * time.Hour: true} {
		mirrors["fresh"].date = now.Add(-lag)
		if why := aptMirrorProblem(context.Background(), ubuntuRoot(t, primary, now), primary, fresh); (why == "") != usable {
			t.Errorf("%s behind: %q, usable want %v", lag, why, usable)
		}
	}
}

// A mirror without one of the suites, without this CPU's index, one that is
// down, or a network that answers with a web page: not switched to. When no
// mirror is usable the sources stay where they are.
func TestUnusableMirrorsAreNotSwitchedTo(t *testing.T) {
	realProbe(t)
	old := mirrorProbeTimeout
	mirrorProbeTimeout = 2 * time.Second
	t.Cleanup(func() { mirrorProbeTimeout = old })
	now := time.Date(2026, 11, 10, 16, 40, 0, 0, time.UTC)
	mirrors := map[string]*aptMirror{
		"nosuite": {date: now, noSuite: "noble-security"},
		"noarch":  {date: now, noArch: true},
		"signin":  {date: now, signIn: true},
	}
	srv := fakeArchive(t, mirrors)
	primary := srv.URL + "/primary/"
	root := ubuntuRoot(t, primary, now)
	dead := "http://127.0.0.1:1/ubuntu-ports/"
	for to, want := range map[string]string{
		srv.URL + "/nosuite/": "noble-security: no Release file (HTTP 404",
		srv.URL + "/noarch/":  "noble: no " + debArch(sys.DetectArch()) + " package index (HTTP 404)",
		srv.URL + "/signin/":  "noble: its Release file has no date",
		srv.URL + "/gone/":    "noble: no Release file (HTTP 404",
		dead:                  "noble: no Release file (",
	} {
		if why := aptMirrorProblem(context.Background(), root, primary, to); !strings.Contains(why, want) {
			t.Errorf("%s: %q, want %q", to, why, want)
		}
	}
	// A base no source uses: nothing to check (as before the probe).
	if why := aptMirrorProblem(context.Background(), root, "http://archive.ubuntu.com/ubuntu/", dead); why != "" {
		t.Errorf("unused base: %q", why)
	}

	fam, _ := pkgmgr.Get("apt")
	lg := NewLog("test")
	defer lg.Close()
	before := sourcesOf(root)
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, fam, nil, lg)
	p.sleep = func(time.Duration) {}
	p.mirrors, p.cur, p.home = [][]string{{primary, srv.URL + "/nosuite/", dead, srv.URL + "/signin/"}}, []int{0}, []int{0}
	if p.switchMirror(context.Background(), nopRep{}, false) {
		t.Error("switched to an unusable mirror")
	}
	if sourcesOf(root) != before || p.cur[0] != 0 || p.switched {
		t.Errorf("the sources moved: cur=%v switched=%v\n%s", p.cur, p.switched, sourcesOf(root))
	}
	// The phone's network is gone (support #5242's first run): the retries go
	// on against the distro's own server, and the sources still name it.
	f := &pkgmgr.Failure{Kind: pkgmgr.KindConnect, Detail: "Network is unreachable", Err: errors.New("exit status 100")}
	if !p.recover(context.Background(), f, 2, false, nopRep{}) {
		t.Error("no retry after a connect failure with no usable mirror")
	}
	if sourcesOf(root) != before {
		t.Errorf("the sources moved:\n%s", sourcesOf(root))
	}
}

// Only apt is asked: the other families switch as before.
func TestMirrorProbeOnlyForApt(t *testing.T) {
	old := mirrorProbe
	mirrorProbe = func(context.Context, string, string, string) string { return "never usable" }
	t.Cleanup(func() { mirrorProbe = old })
	root := t.TempDir()
	repo := filepath.Join(root, "etc/xbps.d/00-repository-main.conf")
	os.MkdirAll(filepath.Dir(repo), 0o755)
	os.WriteFile(repo, []byte("repository=https://repo-default.voidlinux.org/current/aarch64\n"), 0o644)
	fam, _ := pkgmgr.Get("xbps")
	lg := NewLog("test")
	defer lg.Close()
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, fam, nil, lg)
	p.mirrors, p.cur, p.home = [][]string{{"https://repo-default.voidlinux.org/", "https://repo-fastly.voidlinux.org/"}}, []int{0}, []int{0}
	if !p.nextMirror(context.Background(), nopRep{}) {
		t.Fatal("xbps didn't switch")
	}
	if b, _ := os.ReadFile(repo); !strings.Contains(string(b), "repo-fastly") {
		t.Errorf("sources: %s", b)
	}
	// apt with the same probe: no switch.
	aptRoot := ubuntuRoot(t, "http://ports.ubuntu.com/ubuntu-ports/", time.Time{})
	apt, _ := pkgmgr.Get("apt")
	q := newPkgOps(&proot.Target{Rootfs: aptRoot, Local: true}, apt, nil, lg)
	q.mirrors, q.cur, q.home = [][]string{{"http://ports.ubuntu.com/ubuntu-ports/", "http://mirrors.tuna.tsinghua.edu.cn/ubuntu-ports/"}}, []int{0}, []int{0}
	if q.nextMirror(context.Background(), nopRep{}) {
		t.Error("apt switched to a mirror the probe refused")
	}
}

// fakeAptGet puts an apt-get on PATH that only records its arguments, so the
// "-f install" repair never reaches a real package manager in tests.
func fakeAptGet(t *testing.T) string {
	t.Helper()
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	os.WriteFile(filepath.Join(bin, "apt-get"), []byte("#!/bin/sh\necho \"$@\" >> "+log+"\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

// listsFor is a shell command that gives apt lists for base, as a refresh
// that reached it does.
func listsFor(root, base, suite string) string {
	name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(base, "http://"), "/"), "/", "_")
	dir := filepath.Join(root, "var/lib/apt/lists")
	return fmt.Sprintf("mkdir -p %s && printf 'Origin: Ubuntu\\nDate: Thu, 12 Nov 2026 20:47:45 UTC\\n' > %s", dir, filepath.Join(dir, name+"_dists_"+suite+"_InRelease"))
}

// #399, support #5242's second run: an earlier run left the sources on a
// fallback mirror that is behind, and the desktop's packages have unmet
// dependencies there. After apt's own repair doesn't help, the sources go
// back to the distro's own server, the index is refreshed, and the install
// is tried again.
func TestUnmetDepsOnFallbackGoBackToPrimary(t *testing.T) {
	primary, fallback := "http://ports.ubuntu.com/ubuntu-ports/", "http://mirrors.ocf.berkeley.edu/ubuntu-ports/"
	unmet := `echo "The following packages have unmet dependencies:"; echo "E: Unable to correct problems, you have held broken packages."; exit 100`
	setup := func(t *testing.T, update func(root string) string) (*pkgOps, *Logger, string) {
		root := ubuntuRoot(t, fallback, time.Date(2026, 11, 8, 7, 49, 0, 0, time.UTC))
		famReal, _ := pkgmgr.Get("apt")
		fam := *famReal
		fam.Update = update(root)
		lg := NewLog("test")
		p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, nil, lg)
		p.sleep = func(time.Duration) {}
		p.mirrors, p.cur, p.home = [][]string{{primary, "http://ftp.tu-chemnitz.de/pub/linux/ubuntu-ports/", fallback}}, []int{0}, []int{0}
		return p, lg, root
	}
	broken := func(t *testing.T, err error) {
		t.Helper()
		var f *pkgmgr.Failure
		if !errors.As(err, &f) || f.Kind != pkgmgr.KindBroken {
			t.Fatalf("want broken_deps, got %v", err)
		}
	}

	t.Run("install works on the primary", func(t *testing.T) {
		calls := fakeAptGet(t)
		marker := filepath.Join(t.TempDir(), "refreshed")
		p, lg, root := setup(t, func(root string) string { return "touch " + marker + " && " + listsFor(root, primary, "noble") })
		// The install only works once the sources are back on the primary
		// and the index was refreshed for it.
		cmd := fmt.Sprintf("grep -q %s %s && [ -e %s ] && exit 0; %s", primary, filepath.Join(root, "etc/apt/sources.list.d/ubuntu.sources"), marker, unmet)
		err := p.do(context.Background(), cmd, 0, nopRep{}, false)
		lg.Close()
		if err != nil {
			t.Fatalf("the install didn't recover: %v", err)
		}
		if s := sourcesOf(root); strings.Contains(s, fallback) || strings.Count(s, primary) != 2 {
			t.Errorf("sources:\n%s", s)
		}
		if b, _ := os.ReadFile(calls); !strings.Contains(string(b), "-f install") {
			t.Errorf("apt's own repair wasn't tried first: %q", b)
		}
		b, _ := os.ReadFile(lg.Path)
		if !strings.Contains(string(b), "switched the sources back to "+primary) || !strings.Contains(string(b), "worked on attempt 3") {
			t.Errorf("log:\n%s", b)
		}
	})

	// The distro's own server doesn't answer (the install is on the mirror
	// because of that): nothing moves, the install stays on the mirror it
	// can reach, and the step stops with the unmet dependencies.
	t.Run("primary doesn't answer", func(t *testing.T) {
		fakeAptGet(t)
		old := mirrorProbe
		var asked []string
		mirrorProbe = func(_ context.Context, _, from, to string) string {
			asked = append(asked, from+" -> "+to)
			return "noble: no Release file (connection refused)"
		}
		t.Cleanup(func() { mirrorProbe = old })
		refreshed := filepath.Join(t.TempDir(), "refreshed")
		p, lg, root := setup(t, func(string) string { return "touch " + refreshed })
		before := sourcesOf(root)
		err := p.do(context.Background(), unmet, 0, nopRep{}, false)
		lg.Close()
		broken(t, err)
		if sourcesOf(root) != before || p.switched {
			t.Errorf("the sources moved (switched=%v):\n%s", p.switched, sourcesOf(root))
		}
		if len(asked) != 1 || asked[0] != fallback+" -> "+primary {
			t.Errorf("probes: %q", asked)
		}
		if _, e := os.Stat(refreshed); e == nil {
			t.Error("the index was refreshed though nothing moved")
		}
	})

	// The server answered the check, but the refresh didn't reach it. Real
	// apt-get update exits 0 then ("W: Failed to fetch") and leaves no lists
	// for it: the sources go back to the mirror, whose lists are still on
	// disk, instead of leaving the install on a server with no index.
	t.Run("refresh leaves no lists for the primary", func(t *testing.T) {
		fakeAptGet(t)
		p, lg, root := setup(t, func(string) string {
			return `echo "W: Failed to fetch http://ports.ubuntu.com/ubuntu-ports/dists/noble/InRelease  Could not connect to ports.ubuntu.com:80"; exit 0`
		})
		before := sourcesOf(root)
		err := p.do(context.Background(), unmet, 0, nopRep{}, false)
		lg.Close()
		broken(t, err)
		if !p.indexStale || sourcesOf(root) != before || p.cur[0] != 2 {
			t.Errorf("indexStale=%v cur=%v sources:\n%s", p.indexStale, p.cur, sourcesOf(root))
		}
		if b, _ := os.ReadFile(lg.Path); !strings.Contains(string(b), "the refresh left no package lists for "+primary) || strings.Count(string(b), "attempt") != 2 {
			t.Errorf("log:\n%s", b)
		}
		// Once a run: a second failure doesn't try again.
		if p.backToPrimary(context.Background(), nopRep{}, false) {
			t.Error("went back to the primary twice")
		}
	})

	// Unmet dependencies on the distro's own server: as before, apt's repair
	// once, then stop. No mirror is touched.
	t.Run("already on the primary", func(t *testing.T) {
		calls := fakeAptGet(t)
		root := ubuntuRoot(t, primary, time.Time{})
		fam, _ := pkgmgr.Get("apt")
		lg := NewLog("test")
		p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, fam, nil, lg)
		p.sleep = func(time.Duration) {}
		p.mirrors, p.cur, p.home = [][]string{{primary, fallback}}, []int{0}, []int{0}
		before := sourcesOf(root)
		err := p.do(context.Background(), unmet, 0, nopRep{}, false)
		lg.Close()
		broken(t, err)
		if sourcesOf(root) != before || p.switched {
			t.Errorf("the sources moved (switched=%v):\n%s", p.switched, sourcesOf(root))
		}
		if b, _ := os.ReadFile(calls); strings.Count(string(b), "-f install") != 1 {
			t.Errorf("apt's repair ran %q, want once", b)
		}
		if b, _ := os.ReadFile(lg.Path); strings.Count(string(b), "attempt") != 2 {
			t.Errorf("want 2 attempts:\n%s", b)
		}
	})
}

// A mirror switch whose refresh exits 0 without reaching the new mirror
// (apt's "W: Failed to fetch") counts as a failed refresh: the step stops
// with the network failure instead of "Unable to locate package" (#341),
// and the sources go back to the server whose lists are on disk, so the
// next command (the install without its extras) doesn't run without an index.
func TestSwitchNeedsListsFromTheNewMirror(t *testing.T) {
	primary, next := "http://ports.ubuntu.com/ubuntu-ports/", "http://ftp.tu-chemnitz.de/pub/linux/ubuntu-ports/"
	failed := `echo "W: Failed to fetch ` + next + `dists/noble/InRelease  Could not connect to ftp.tu-chemnitz.de:80"`
	for _, c := range []struct {
		name   string
		update func(root string) string
		stale  bool
	}{
		{"lists arrive", func(root string) string { return listsFor(root, next, "noble") }, false},
		{"no lists", func(string) string { return "true" }, true},
		// Lists from an earlier visit to this mirror are on disk, and apt
		// says this refresh didn't reach it.
		{"old lists, refresh failed", func(root string) string { return listsFor(root, next, "noble") + " && " + failed }, true},
		// Another suite failing isn't the whole mirror failing.
		{"another suite failed", func(root string) string {
			return listsFor(root, next, "noble") + ` && echo "W: Failed to fetch ` + next + `dists/noble-backports/InRelease  Connection timed out"`
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := ubuntuRoot(t, primary, time.Time{})
			before := sourcesOf(root)
			famReal, _ := pkgmgr.Get("apt")
			fam := *famReal
			fam.Update = c.update(root)
			lg := NewLog("test")
			defer lg.Close()
			p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, nil, lg)
			p.mirrors, p.cur, p.home = [][]string{{primary, next}}, []int{0}, []int{0}
			if !p.switchMirror(context.Background(), nopRep{}, false) {
				t.Fatal("no switch")
			}
			if p.indexStale != c.stale {
				t.Fatalf("indexStale=%v, want %v", p.indexStale, c.stale)
			}
			if c.stale && (sourcesOf(root) != before || p.cur[0] != 0) {
				t.Errorf("after a failed refresh the sources aren't back on the primary (cur=%v):\n%s", p.cur, sourcesOf(root))
			}
			if !c.stale && (strings.Count(sourcesOf(root), next) != 2 || p.cur[0] != 1) {
				t.Errorf("sources (cur=%v):\n%s", p.cur, sourcesOf(root))
			}
		})
	}

	// Debian's two groups: both move, both go back.
	d, famReal := loadDistroFam(t, "debian")
	root := t.TempDir()
	src := filepath.Join(root, "etc/apt/sources.list.d/debian.sources")
	os.MkdirAll(filepath.Dir(src), 0o755)
	body := "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie trixie-updates\nComponents: main\n\nTypes: deb\nURIs: http://deb.debian.org/debian-security\nSuites: trixie-security\nComponents: main\n"
	os.WriteFile(src, []byte(body), 0o644)
	fam := *famReal
	fam.Update = "true"
	lg := NewLog("test")
	defer lg.Close()
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, d, lg)
	if !p.switchMirror(context.Background(), nopRep{}, false) || !p.indexStale {
		t.Fatalf("debian: indexStale=%v", p.indexStale)
	}
	if b, _ := os.ReadFile(src); string(b) != body || p.cur[0] != 0 || p.cur[1] != 0 {
		t.Errorf("debian after the undo (cur=%v):\n%s", p.cur, b)
	}
}

// One command asks a mirror that failed the check only once, however many
// times it retries (a phone with no network would otherwise wait for every
// mirror at every retry); the next command asks again.
func TestFailedProbeIsRememberedForOneCommand(t *testing.T) {
	fakeAptGet(t)
	old := mirrorProbe
	asked := map[string]int{}
	mirrorProbe = func(_ context.Context, _, _, to string) string {
		asked[hostOf(to)]++
		return "noble: no Release file (timeout)"
	}
	t.Cleanup(func() { mirrorProbe = old })
	primary := "http://ports.ubuntu.com/ubuntu-ports/"
	root := ubuntuRoot(t, primary, time.Time{})
	fam, _ := pkgmgr.Get("apt")
	lg := NewLog("test")
	defer lg.Close()
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, fam, nil, lg)
	p.sleep = func(time.Duration) {}
	p.mirrors, p.cur, p.home = [][]string{{primary, "http://ftp.tu-chemnitz.de/pub/linux/ubuntu-ports/", "http://mirrors.tuna.tsinghua.edu.cn/ubuntu-ports/"}}, []int{0}, []int{0}
	connectFail := `echo "E: Failed to fetch ports.ubuntu.com Unable to connect to ports.ubuntu.com:80:"; exit 100`
	before := sourcesOf(root)
	for run := 1; run <= 2; run++ {
		err := p.do(context.Background(), connectFail, 0, nopRep{}, false)
		var f *pkgmgr.Failure
		if !errors.As(err, &f) || f.Kind != pkgmgr.KindConnect {
			t.Fatalf("command %d: want a connect failure, got %v", run, err)
		}
		if asked["ftp.tu-chemnitz.de"] != run || asked["mirrors.tuna.tsinghua.edu.cn"] != run {
			t.Errorf("after command %d: probes %v, want %d each", run, asked, run)
		}
	}
	if sourcesOf(root) != before {
		t.Errorf("the sources moved:\n%s", sourcesOf(root))
	}
}

// An install an earlier run left on the last mirror of the list (Ubuntu's
// first fallback until 2.1.5, last since) still has somewhere to go when
// that mirror fails: the distro's own server, then the mirrors between. A
// run never goes back to a server it moved away from.
func TestLastMirrorGoesBackUpTheList(t *testing.T) {
	a, b, c := "http://ports.ubuntu.com/ubuntu-ports/", "http://ftp.tu-chemnitz.de/pub/linux/ubuntu-ports/", "http://mirrors.ocf.berkeley.edu/ubuntu-ports/"
	root := ubuntuRoot(t, c, time.Time{})
	fam, _ := pkgmgr.Get("apt")
	lg := NewLog("test")
	defer lg.Close()
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, fam, nil, lg)
	p.mirrors, p.cur, p.home = [][]string{{a, b, c}}, []int{0}, []int{0}
	var hosts []string
	for p.nextMirror(context.Background(), nopRep{}) {
		hosts = append(hosts, hostOf(p.mirrors[0][p.cur[0]]))
		if len(hosts) > 5 {
			break
		}
	}
	if strings.Join(hosts, " ") != "ports.ubuntu.com ftp.tu-chemnitz.de" {
		t.Errorf("from the last mirror: %q", hosts)
	}
	if !strings.Contains(sourcesOf(root), b) {
		t.Errorf("sources:\n%s", sourcesOf(root))
	}

	// Kali: the server before its own (kali.download, for installs an old
	// version rewrote) is never gone to.
	d, kfam := loadDistroFam(t, "kali")
	kroot := t.TempDir()
	src := filepath.Join(kroot, "etc/apt/sources.list.d/kali.sources")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.WriteFile(src, []byte("Types: deb\nURIs: http://mirrors.tuna.tsinghua.edu.cn/kali/\nSuites: kali-rolling\nComponents: main\n"), 0o644)
	q := newPkgOps(&proot.Target{Rootfs: kroot, Local: true}, kfam, d, lg)
	hosts = nil
	for q.nextMirror(context.Background(), nopRep{}) {
		hosts = append(hosts, hostOf(q.mirrors[0][q.cur[0]]))
		if len(hosts) > 6 {
			break
		}
	}
	if strings.Join(hosts, " ") != "http.kali.org mirrors.ocf.berkeley.edu kali.mirror.garr.it" {
		t.Errorf("kali from the last mirror: %q", hosts)
	}

	// A cancelled run asks no mirror.
	old := mirrorProbe
	asked := 0
	mirrorProbe = func(context.Context, string, string, string) string { asked++; return "" }
	t.Cleanup(func() { mirrorProbe = old })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r2 := newPkgOps(&proot.Target{Rootfs: ubuntuRoot(t, a, time.Time{}), Local: true}, fam, nil, lg)
	r2.mirrors, r2.cur, r2.home = [][]string{{a, b, c}}, []int{0}, []int{0}
	if r2.nextMirror(ctx, nopRep{}) || asked != 0 {
		t.Errorf("after a cancel: asked %d mirrors", asked)
	}
}

func loadDistroFam(t *testing.T, id string) (*conf.Distro, *pkgmgr.Family) {
	t.Helper()
	d, err := conf.LoadDistro(id)
	if err != nil {
		t.Fatal(err)
	}
	fam, _ := pkgmgr.Get(d.Family)
	return d, fam
}

// Kali's own server is the second URL of its list (the first is one older
// installs were rewritten to): "back to the primary" goes there, and an
// install still on the first URL isn't treated as being on a fallback.
func TestKaliPrimaryIsTheRedirector(t *testing.T) {
	d, fam := loadDistroFam(t, "kali")
	lg := NewLog("test")
	defer lg.Close()
	root := t.TempDir()
	src := filepath.Join(root, "etc/apt/sources.list.d/kali.sources")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.WriteFile(src, []byte("Types: deb\nURIs: http://mirrors.ocf.berkeley.edu/kali/\nSuites: kali-rolling\nComponents: main\n"), 0o644)
	f2 := *fam
	f2.Update = listsFor(root, "http://http.kali.org/kali/", "kali-rolling")
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &f2, d, lg)
	if !p.backToPrimary(context.Background(), nopRep{}, false) {
		t.Fatal("didn't go back")
	}
	if b, _ := os.ReadFile(src); !strings.Contains(string(b), "URIs: http://http.kali.org/kali/\n") {
		t.Errorf("sources: %s", b)
	}
	os.WriteFile(src, []byte("Types: deb\nURIs: http://kali.download/kali/\nSuites: kali-rolling\nComponents: main\n"), 0o644)
	q := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &f2, d, lg)
	if q.backToPrimary(context.Background(), nopRep{}, false) {
		t.Error("kali.download was treated as a fallback mirror")
	}
	// Every other distro's own server is the first URL of each group.
	for _, id := range []string{"ubuntu", "ubuntu24", "debian", "void"} {
		d, fam := loadDistroFam(t, id)
		for g, h := range newPkgOps(&proot.Target{Rootfs: root}, fam, d, lg).home {
			if h != 0 {
				t.Errorf("%s group %d: home %d", id, g, h)
			}
		}
	}
}
