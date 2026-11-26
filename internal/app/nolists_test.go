package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// "Installing the basics" on Kali, Debian and Ubuntu, Nov 2026: 37 of 71
// failure reports (auto-ticket #1092, support #5586 #5593 #5783) were apt
// without package lists from the server the sources name, after a refresh
// that had passed. The step ended with "E: Package adduser has no
// installation candidate", twice, in 2 to 4 seconds.

// basicsTitle is the title install gives a failed "Installing the basics".
const basicsTitle = "Couldn't install the basics"

// noCandidate is apt-get install for a package apt has no version of, as
// apt 3 prints it (exit status 100).
const noCandidate = `echo "Package adduser is not available, but is referred to by another package."
echo "This may mean that the package is missing, has been obsoleted, or"
echo "is only available from another source"
echo
echo "E: Package 'adduser' has no installation candidate"
exit 100`

// aptDistro is one of the three apt distros as its image has it: the sources
// file, the main archive's own server, its first suite, and the mirror the
// sources move to first.
type aptDistro struct {
	id, file, sources, primary, suite, next string
}

var aptDistros = []aptDistro{
	{"kali", "kali.sources", "Types: deb\nURIs: http://http.kali.org/kali/\nSuites: kali-rolling\nComponents: main contrib non-free non-free-firmware\nSigned-By: /usr/share/keyrings/kali-archive-keyring.gpg\n",
		"http://http.kali.org/kali/", "kali-rolling", "http://mirrors.ocf.berkeley.edu/kali/"},
	{"debian", "debian.sources", debianSources,
		"http://deb.debian.org/debian", "trixie", "http://ftp.debian.org/debian"},
	{"ubuntu", "ubuntu.sources", "Types: deb\nURIs: http://ports.ubuntu.com/ubuntu-ports/\nSuites: noble noble-updates noble-backports\nComponents: main universe restricted multiverse\n\n" +
		"Types: deb\nURIs: http://ports.ubuntu.com/ubuntu-ports/\nSuites: noble-security\nComponents: main universe restricted multiverse\n",
		"http://ports.ubuntu.com/ubuntu-ports/", "noble", "http://ftp.tu-chemnitz.de/pub/linux/ubuntu-ports/"},
}

// noLists is a distro's install with its image's sources and no package
// lists at all. update is what a refresh does (a shell script; every run adds
// a line to refreshes); the basics' install finds adduser once apt has lists
// from has (a server's base URL), and every run of it adds a line to installs.
type noLists struct {
	aptDistro
	p                        *pkgOps
	d                        *conf.Distro
	root, src                string
	refreshes, installs, cmd string
	logged                   func() string
}

func newNoLists(t *testing.T, a aptDistro, has string, update func(n *noLists) string) *noLists {
	t.Helper()
	t.Cleanup(func() { pmSaved = "" })
	d, famReal := loadDistroFam(t, a.id)
	n := &noLists{aptDistro: a, d: d, root: t.TempDir()}
	n.src = filepath.Join(n.root, "etc/apt/sources.list.d", a.file)
	os.MkdirAll(filepath.Dir(n.src), 0o755)
	os.WriteFile(n.src, []byte(a.sources), 0o644)
	tmp := t.TempDir()
	n.refreshes, n.installs = filepath.Join(tmp, "refreshes"), filepath.Join(tmp, "installs")
	fam := *famReal
	fam.Update, fam.Repair = "echo run >>"+n.refreshes+"\n"+update(n), ""
	n.cmd = "echo run >>" + n.installs + "\n[ -e " + n.packages(has) + " ] && exit 0\n" + noCandidate
	lg, buf := bufLog()
	n.logged = buf.String
	n.p = newPkgOps(&proot.Target{Rootfs: n.root, Local: true}, &fam, d, lg)
	n.p.sleep = func(time.Duration) {}
	return n
}

// release is the Release file apt has on disk for base's first suite.
func (n *noLists) release(base string) string {
	name := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(base, "http://"), "/"), "/", "_")
	return filepath.Join(n.root, "var/lib/apt/lists", name+"_dists_"+n.suite+"_InRelease")
}

// packages is the package index apt has on disk next to release(base).
func (n *noLists) packages(base string) string {
	return strings.TrimSuffix(n.release(base), "InRelease") + "main_binary-arm64_Packages.lz4"
}

// arrive is a shell command that gives apt base's lists, as a refresh that
// reached base does.
func (n *noLists) arrive(base string) string {
	return listsFor(n.root, base, n.suite) + " && : >" + n.packages(base)
}

// fetch is a refresh that brings base's lists when the sources name base.
func (n *noLists) fetch(base string) string {
	return `grep -q "URIs: ` + base + `$" ` + n.src + ` && { ` + n.arrive(base) + `; }; exit 0`
}

// older puts lists from an earlier refresh of base on disk.
func (n *noLists) older(base string) {
	os.MkdirAll(filepath.Dir(n.release(base)), 0o755)
	os.WriteFile(n.release(base), []byte("Origin: x\nDate: Sun, 08 Nov 2026 07:49:44 UTC\n"), 0o644)
	os.WriteFile(n.packages(base), nil, 0o644)
}

func (n *noLists) sources() string {
	b, _ := os.ReadFile(n.src)
	return string(b)
}

// basics runs the step's package install, as install does.
func (n *noLists) basics() error {
	return n.p.run(context.Background(), n.cmd, 7, nopRep{})
}

// The refresh hadn't brought the lists; one more does. The install is tried
// again and works, on the server the sources were on.
func TestNoCandidateRefreshesTheLists(t *testing.T) {
	for _, a := range aptDistros {
		t.Run(a.id, func(t *testing.T) {
			n := newNoLists(t, a, a.primary, func(n *noLists) string { return n.fetch(a.primary) })
			if err := n.basics(); err != nil {
				t.Fatalf("the install didn't recover: %v\n%s", err, n.logged())
			}
			if r, i := len(callsOf(n.refreshes)), len(callsOf(n.installs)); r != 1 || i != 2 {
				t.Errorf("%d refreshes and %d installs, want 1 and 2\n%s", r, i, n.logged())
			}
			if n.sources() != a.sources || n.p.switched {
				t.Errorf("the sources moved (switched=%v):\n%s", n.p.switched, n.sources())
			}
			for _, s := range []string{"attempt 1 failed: package_not_found: E: Package adduser has no installation candidate", "refreshing the package lists", "the retry with fresh package lists worked", "worked on attempt 2"} {
				if !strings.Contains(n.logged(), s) {
					t.Errorf("log lacks %q:\n%s", s, n.logged())
				}
			}
			if pmSaved != "package_not_found_retried" {
				t.Errorf("pmSaved = %q", pmSaved)
			}
		})
	}
}

// The refresh brings nothing from the server the sources name, and says
// nothing about it (it exits 0): the main archive moves to the next mirror,
// whose lists do arrive, and the install is tried again there. Debian's
// security archive, on a server of its own, stays where it is.
func TestNoCandidateMovesToAMirrorWithLists(t *testing.T) {
	for _, a := range aptDistros {
		t.Run(a.id, func(t *testing.T) {
			n := newNoLists(t, a, a.next, func(n *noLists) string { return n.fetch(a.next) })
			if err := n.basics(); err != nil {
				t.Fatalf("the install didn't recover: %v\n%s", err, n.logged())
			}
			want := strings.ReplaceAll(a.sources, "URIs: "+a.primary+"\n", "URIs: "+a.next+"\n")
			if n.sources() != want {
				t.Errorf("sources:\n%s\nwant\n%s\n%s", n.sources(), want, n.logged())
			}
			// The refresh for the "not found", then the one for the new mirror.
			if r, i := len(callsOf(n.refreshes)), len(callsOf(n.installs)); r != 2 || i != 2 {
				t.Errorf("%d refreshes and %d installs, want 2 and 2\n%s", r, i, n.logged())
			}
			for _, s := range []string{"apt has no package lists from " + strings.TrimSuffix(a.primary, "/") + "/dists/" + a.suite + "/ after the refresh", "switched the sources from " + a.primary + " to " + a.next, "the retry with fresh package lists worked"} {
				if !strings.Contains(n.logged(), s) {
					t.Errorf("log lacks %q:\n%s", s, n.logged())
				}
			}
			if !n.p.switched || n.p.indexStale || n.p.moveOnly != nil {
				t.Errorf("switched=%v indexStale=%v moveOnly=%v", n.p.switched, n.p.indexStale, n.p.moveOnly)
			}
			if a.id == "debian" && n.p.cur[1] != 0 {
				t.Errorf("cur = %v: the security archive moved too", n.p.cur)
			}
		})
	}
}

// No lists arrive from the mirror either: the switch doesn't count (the
// sources go back), and the step ends with "Couldn't reach the package
// servers", not "adduser has no installation candidate". The step's second
// run of the install (without the app's display packages) gets the same
// error without another round of refreshes.
func TestNoCandidateWithoutListsIsTheNetwork(t *testing.T) {
	for _, a := range aptDistros {
		t.Run(a.id, func(t *testing.T) {
			n := newNoLists(t, a, a.primary, func(*noLists) string { return "exit 0" })
			err := n.basics()
			var f *pkgmgr.Failure
			if !errors.As(err, &f) || f.Kind != pkgmgr.KindConnect || !f.Lists {
				t.Fatalf("want a connect failure of the lists, got %v\n%s", err, n.logged())
			}
			if want := "no package lists from " + hostOf(a.primary); f.Detail != want {
				t.Errorf("detail %q, want %q", f.Detail, want)
			}
			if n.sources() != a.sources {
				t.Errorf("the sources aren't back where they were:\n%s\n%s", n.sources(), n.logged())
			}
			if r, i := len(callsOf(n.refreshes)), len(callsOf(n.installs)); r != 2 || i != 1 {
				t.Errorf("%d refreshes and %d installs, want 2 and 1\n%s", r, i, n.logged())
			}
			e := pkgErr(basicsTitle, err).(*ui.UserError)
			if e.Title != "Couldn't reach the package servers" || e.Kind != "network_connect" || e.Class != "package_manager" ||
				!strings.Contains(e.What, "didn't answer") || !strings.Contains(e.Fix, "Check your internet") || strings.Contains(e.What, "installation candidate") {
				t.Errorf("user error: %q %q %q\n%s\n%s", e.Title, e.Kind, e.Class, e.What, e.Fix)
			}

			err = n.basics()
			if !errors.As(err, &f) || f.Kind != pkgmgr.KindConnect || !f.Lists {
				t.Errorf("the second run: %v\n%s", err, n.logged())
			}
			if r, i := len(callsOf(n.refreshes)), len(callsOf(n.installs)); r != 2 || i != 2 {
				t.Errorf("after the second run: %d refreshes and %d installs, want 2 and 2", r, i)
			}
		})
	}
}

// What the refresh said is the error: a lookup that failed (apt 2.8 warns
// and exits 0), apt 3 with the phone offline (exit status 100, "does not
// have a Release file"), and no mirror answers the check before a switch.
func TestNoCandidateNamesTheRefreshFailure(t *testing.T) {
	for _, c := range []struct {
		name   string
		a      aptDistro
		update string
		kind   pkgmgr.Kind
		title  string
		what   string
	}{
		{"lookup fails", aptDistros[0], `echo "Ign:1 http://http.kali.org/kali kali-rolling InRelease"
echo "W: Failed to fetch http://http.kali.org/kali/dists/kali-rolling/InRelease  Temporary failure resolving 'http.kali.org'"
echo "W: Some index files failed to download. They have been ignored, or old ones used instead."
exit 0`, pkgmgr.KindDNS, "Couldn't reach the package servers", "Temporary failure resolving"},
		{"apt 3 offline", aptDistros[2], aptOffline, pkgmgr.KindConnect, "Couldn't reach the package servers", "Network is unreachable"},
		{"a mirror without its index", aptDistros[1], `echo "Err:4 http://deb.debian.org/debian trixie Release"
echo "  404  Not Found [IP: 151.101.2.132 80]"
echo "E: The repository 'http://deb.debian.org/debian trixie Release' does not have a Release file."
exit 100`, pkgmgr.KindMirror, "The package mirrors sent a broken answer", "no package index"},
	} {
		t.Run(c.name, func(t *testing.T) {
			refuseMirrors(t)
			n := newNoLists(t, c.a, c.a.primary, func(*noLists) string { return c.update })
			err := n.basics()
			var f *pkgmgr.Failure
			if !errors.As(err, &f) || f.Kind != c.kind || !f.Lists {
				t.Fatalf("want %s, got %v\n%s", c.kind, err, n.logged())
			}
			if n.sources() != c.a.sources {
				t.Errorf("the sources moved:\n%s", n.sources())
			}
			if r, i := len(callsOf(n.refreshes)), len(callsOf(n.installs)); r != 1 || i != 1 {
				t.Errorf("%d refreshes and %d installs, want 1 and 1\n%s", r, i, n.logged())
			}
			e := pkgErr(basicsTitle, err).(*ui.UserError)
			if e.Title != c.title || e.Kind != string(c.kind) || !strings.Contains(e.What, c.what) {
				t.Errorf("user error: %q %q\n%s", e.Title, e.Kind, e.What)
			}
		})
	}
}

// A package the archive really doesn't have, with the main archive's lists
// on disk: one refresh, one more try, and the error stays apt's own. No
// mirror is asked and the sources don't move.
func TestMissingPackageStaysMissing(t *testing.T) {
	asked := 0
	old := mirrorProbe
	mirrorProbe = func(context.Context, string, string, string) string { asked++; return "" }
	t.Cleanup(func() { mirrorProbe = old })
	a := aptDistros[2]
	n := newNoLists(t, a, a.primary, func(n *noLists) string { return n.fetch(a.primary) })
	n.cmd = `echo run >>` + n.installs + `; echo "Reading package lists..."; echo "E: Unable to locate package tigervnc-tools"; exit 100`
	n.older(a.primary)

	err := n.basics()
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindNotFound || f.Lists {
		t.Fatalf("want package_not_found, got %v\n%s", err, n.logged())
	}
	if r, i := len(callsOf(n.refreshes)), len(callsOf(n.installs)); r != 1 || i != 2 {
		t.Errorf("%d refreshes and %d installs, want 1 and 2\n%s", r, i, n.logged())
	}
	if asked != 0 || n.sources() != a.sources || n.p.switched {
		t.Errorf("asked %d mirrors, switched=%v, sources:\n%s", asked, n.p.switched, n.sources())
	}
	if !strings.Contains(n.logged(), "the retry with fresh package lists didn't help (package_not_found)") || pmSaved != "" {
		t.Errorf("pmSaved=%q log:\n%s", pmSaved, n.logged())
	}
	e := pkgErr("Couldn't install XFCE", err).(*ui.UserError)
	if e.Title != "Couldn't install XFCE" || e.Kind != "package_not_found" || !strings.Contains(e.What, "Unable to locate package tigervnc-tools") {
		t.Errorf("user error: %q %q\n%s", e.Title, e.Kind, e.What)
	}
	// The next command of the run that doesn't find a package: no refresh.
	if err := n.basics(); !errors.As(err, &f) || f.Kind != pkgmgr.KindNotFound || len(callsOf(n.refreshes)) != 1 || len(callsOf(n.installs)) != 3 {
		t.Errorf("the next command: %v, %d refreshes, %d installs", err, len(callsOf(n.refreshes)), len(callsOf(n.installs)))
	}
}

// Older lists are on disk and the refresh says it didn't reach their server:
// the lists didn't arrive, so the main archive moves on, as with none.
func TestNoCandidateWithOlderListsTheRefreshMissed(t *testing.T) {
	a := aptDistros[0]
	n := newNoLists(t, a, a.next, func(n *noLists) string {
		return `grep -q "URIs: ` + a.next + `$" ` + n.src + ` && { ` + n.arrive(a.next) + `; exit 0; }
echo "W: Failed to fetch ` + a.primary + `dists/` + a.suite + `/InRelease  Could not connect to http.kali.org:80"
exit 0`
	})
	n.older(a.primary)
	if err := n.basics(); err != nil {
		t.Fatalf("%v\n%s", err, n.logged())
	}
	if !strings.Contains(n.sources(), "URIs: "+a.next+"\n") || !strings.Contains(n.logged(), "the refresh didn't reach "+a.primary) {
		t.Errorf("sources:\n%s\n%s", n.sources(), n.logged())
	}
}

// A cancel during the refresh ends the step as cancelled.
func TestNoCandidateCancelled(t *testing.T) {
	a := aptDistros[0]
	ctx, cancel := context.WithCancel(context.Background())
	n := newNoLists(t, a, a.primary, func(*noLists) string { return "exit 0" })
	old := mirrorProbe
	mirrorProbe = func(context.Context, string, string, string) string { cancel(); return "cancelled" }
	t.Cleanup(func() { mirrorProbe = old })
	err := n.p.run(ctx, n.cmd, 7, nopRep{})
	if pkgErr(basicsTitle, err) != ui.ErrCancelled {
		t.Errorf("got %v\n%s", err, n.logged())
	}
	if n.p.noLists != nil {
		t.Error("a cancelled refresh was kept as the run's lists failure")
	}
}

// pack add stops at the first error of a package run, as it always has: no
// refresh, no mirror.
func TestPackDoesntRefreshForAMissingPackage(t *testing.T) {
	a := aptDistros[1]
	n := newNoLists(t, a, a.primary, func(n *noLists) string { return n.fetch(a.primary) })
	n.p.shared = true
	err := n.basics()
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindNotFound || f.Lists {
		t.Fatalf("got %v\n%s", err, n.logged())
	}
	if r, i := len(callsOf(n.refreshes)), len(callsOf(n.installs)); r != 0 || i != 1 {
		t.Errorf("%d refreshes and %d installs, want 0 and 1", r, i)
	}
}

// The refresh step's own ca-certificates install (Debian's and Kali's images
// have no CA store) after an apt-get update that exited 0 without a word and
// without lists: tried again, then on the next mirror; when that brings no
// lists either, the refresh ends with the network error (the sources stay on
// the mirror, as after any refresh that failed there too; the next run starts
// from it and can move back up the list).
func TestRefreshWithoutListsAndWithoutAWord(t *testing.T) {
	notFound := `echo "E: Unable to locate package ca-certificates"; exit 100`
	for _, a := range aptDistros[:2] {
		t.Run(a.id+" moves on", func(t *testing.T) {
			n := newNoLists(t, a, a.primary, func(n *noLists) string {
				return `grep -q "URIs: ` + a.next + `$" ` + n.src + ` && { ` + n.arrive(a.next) + `; exit 0; }
` + notFound
			})
			if err := n.p.update(context.Background(), nopRep{}); err != nil {
				t.Fatalf("%v\n%s", err, n.logged())
			}
			if got := len(callsOf(n.refreshes)); got != 3 {
				t.Errorf("apt ran %d times, want 3 (two on the silent server, one on the next)", got)
			}
			if !strings.Contains(n.sources(), "URIs: "+a.next+"\n") || (a.id == "debian" && !reflect.DeepEqual(n.p.cur, []int{1, 0})) {
				t.Errorf("cur %v, sources:\n%s", n.p.cur, n.sources())
			}
		})
		t.Run(a.id+" no lists anywhere", func(t *testing.T) {
			n := newNoLists(t, a, a.primary, func(*noLists) string { return notFound })
			err := n.p.update(context.Background(), nopRep{})
			var f *pkgmgr.Failure
			if !errors.As(err, &f) || f.Kind != pkgmgr.KindConnect || !f.Lists {
				t.Fatalf("got %v\n%s", err, n.logged())
			}
			if e := pkgErr(refreshTitle, err).(*ui.UserError); e.Title != "Couldn't reach the package servers" || e.Kind != "network_connect" {
				t.Errorf("user error: %q %q", e.Title, e.Kind)
			}
		})
	}
}

// The words are apt's: from another package manager they stay "other", with
// the one retry that has.
func TestNotFoundIsAptsOnly(t *testing.T) {
	fam, _ := pkgmgr.Get("pacman")
	calls := filepath.Join(t.TempDir(), "calls")
	lg, logged := bufLog()
	p := newPkgOps(&proot.Target{Rootfs: t.TempDir(), Local: true}, fam, nil, lg)
	p.sleep = func(time.Duration) {}
	err := p.run(context.Background(), `echo run >>`+calls+`; echo "error: unable to locate package x"; exit 1`, 1, nopRep{})
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindOther || len(callsOf(calls)) != 2 {
		t.Errorf("got %v after %d runs\n%s", err, len(callsOf(calls)), logged)
	}
}

// The review of #13.

// A cancel while a mirror is asked during the refresh step's own "not
// found" ends the step as cancelled, not as the network error.
func TestRefreshNotFoundCancelledAtTheMirrorCheck(t *testing.T) {
	a := aptDistros[0]
	ctx, cancel := context.WithCancel(context.Background())
	n := newNoLists(t, a, a.primary, func(*noLists) string { return `echo "E: Unable to locate package ca-certificates"; exit 100` })
	old := mirrorProbe
	mirrorProbe = func(context.Context, string, string, string) string { cancel(); return "cancelled" }
	t.Cleanup(func() { mirrorProbe = old })
	err := n.p.update(ctx, nopRep{})
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindCancelled || f.Lists || pkgErr(refreshTitle, err) != ui.ErrCancelled {
		t.Errorf("got %v\n%s", err, n.logged())
	}
	if n.sources() != a.sources {
		t.Errorf("the sources moved:\n%s", n.sources())
	}
}

// The main archive's Release file is on disk without a package index, while
// another archive's index is (Debian's security archive), and the refresh
// passes without a word: apt still knows none of the main archive's
// packages, so it moves to the next mirror like an archive without lists.
func TestNoCandidateWithAReleaseFileButNoIndex(t *testing.T) {
	a := aptDistros[1]
	n := newNoLists(t, a, a.next, func(n *noLists) string { return n.fetch(a.next) })
	lists := filepath.Dir(n.release(a.primary))
	os.MkdirAll(lists, 0o755)
	os.WriteFile(n.release(a.primary), []byte("Origin: Debian\nDate: Sun, 08 Nov 2026 07:49:44 UTC\n"), 0o644)
	os.WriteFile(filepath.Join(lists, "deb.debian.org_debian-security_dists_trixie-security_main_binary-arm64_Packages.lz4"), nil, 0o644)
	if err := n.basics(); err != nil {
		t.Fatalf("%v\n%s", err, n.logged())
	}
	if !strings.Contains(n.sources(), "URIs: "+a.next+"\n") || !reflect.DeepEqual(n.p.cur, []int{1, 0}) ||
		!strings.Contains(n.logged(), "apt has no package index from http://deb.debian.org/debian/dists/trixie/ after the refresh") {
		t.Errorf("cur %v, sources:\n%s\n%s", n.p.cur, n.sources(), n.logged())
	}
}

// A refresh that failed for another reason than the server (here gpgv dying
// under proot, a kind with a retry of its own that never ran here) keeps its
// own line: titled as the failed refresh, without that kind's explanation.
func TestNoCandidateKeepsAnotherRefreshFailureAsItIs(t *testing.T) {
	refuseMirrors(t)
	a := aptDistros[2]
	n := newNoLists(t, a, a.primary, func(*noLists) string {
		return `echo "E: Sub-process /usr/bin/gpgv exited unexpectedly"; exit 100`
	})
	err := n.basics()
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindOther || !f.Lists {
		t.Fatalf("got %v\n%s", err, n.logged())
	}
	e := pkgErr(basicsTitle, err).(*ui.UserError)
	if e.Title != refreshTitle || e.Kind != "other" || !strings.Contains(e.What, "exited unexpectedly") || strings.Contains(e.What, "fast mode") {
		t.Errorf("user error: %q %q\n%s", e.Title, e.Kind, e.What)
	}
}

// A full disk during the refresh isn't a mirror's fault: no mirror is asked,
// and the error is the full disk.
func TestNoCandidateWithAFullDisk(t *testing.T) {
	asked := 0
	old := mirrorProbe
	mirrorProbe = func(context.Context, string, string, string) string { asked++; return "" }
	t.Cleanup(func() { mirrorProbe = old })
	a := aptDistros[0]
	n := newNoLists(t, a, a.primary, func(*noLists) string {
		return `echo "E: Write error - write (28: No space left on device)"; exit 100`
	})
	err := n.basics()
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindDiskFull || asked != 0 || n.sources() != a.sources {
		t.Fatalf("got %v, asked %d mirrors\n%s", err, asked, n.logged())
	}
	if e := pkgErr(basicsTitle, err).(*ui.UserError); e.Title != "Your phone ran out of space" || e.Class != "not_enough_space" {
		t.Errorf("user error: %q %q", e.Title, e.Class)
	}
}

// Sources on a server that isn't in the distro's mirror list (the user's
// own), without lists: nothing to move, and the error doesn't name a server
// the sources don't use.
func TestNoCandidateOnTheUsersOwnServer(t *testing.T) {
	a := aptDistros[0]
	a.sources = strings.ReplaceAll(a.sources, a.primary, "http://mirror.example.org/kali/")
	n := newNoLists(t, a, a.primary, func(*noLists) string { return "exit 0" })
	err := n.basics()
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindConnect || !f.Lists || f.Detail != "no package lists from the distro s package server" {
		t.Fatalf("got %v (%+v)\n%s", err, f, n.logged())
	}
	if n.sources() != a.sources || n.p.switched {
		t.Errorf("the sources moved:\n%s", n.sources())
	}
}
