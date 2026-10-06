package app

import (
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

func TestDiagnose(t *testing.T) {
	exit := errors.New("exit status 100")
	for _, c := range []struct {
		lines []string
		kind  pkgmgr.Kind
	}{
		{[]string{"Err:1 http://ports.ubuntu.com/ubuntu-ports resolute InRelease", "  Temporary failure resolving 'ports.ubuntu.com'", "E: Failed to fetch http://ports.ubuntu.com/ubuntu-ports/dists/resolute/InRelease"}, pkgmgr.KindDNS},
		{[]string{"Err:7 http://deb.debian.org/debian trixie/main arm64 libgtk-3-0t64 arm64 3.24.49-3", "  404  Not Found [IP: 151.101.2.132 80]", "E: Failed to fetch http://deb.debian.org/debian/pool/main/g/gtk+3.0/libgtk-3-0t64_3.24.49-3_arm64.deb  404  Not Found"}, pkgmgr.KindHTTP404},
		{[]string{"E: Failed to fetch http://ports.ubuntu.com/ubuntu-ports/dists/resolute-updates/main/binary-arm64/Packages.xz  Hash Sum mismatch"}, pkgmgr.KindHash},
		{[]string{"E: dpkg was interrupted, you must manually run 'dpkg --configure -a' to correct the problem."}, pkgmgr.KindDpkg},
		{[]string{"E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 4242 (apt-get)"}, pkgmgr.KindLock},
		{[]string{"E: Release file for http://ports.ubuntu.com/ubuntu-ports/dists/resolute-updates/InRelease is not valid yet (invalid for another 3h 2min 1s)."}, pkgmgr.KindClock},
		{[]string{"W: GPG error: http://kali.download/kali kali-rolling InRelease: The following signatures were invalid: EXPKEYSIG ED65462EC8D5E4C5", "E: The repository 'http://kali.download/kali kali-rolling InRelease' is not signed."}, pkgmgr.KindGPG},
		{[]string{"The following packages have unmet dependencies:", "E: Unable to correct problems, you have held broken packages."}, pkgmgr.KindBroken},
		{[]string{"dpkg: error processing archive /var/cache/apt/archives/firefox.deb (--unpack):", " cannot copy extracted data for './usr/lib/firefox/libxul.so' to '/usr/lib/firefox/libxul.so.dpkg-new': failed to write (No space left on device)"}, pkgmgr.KindDiskFull},
		{[]string{"ERROR: [reposync] failed to fetch file `https://repo-default.voidlinux.org/current/aarch64/aarch64-repodata': Operation timed out"}, pkgmgr.KindConnect},
		{[]string{"error: failed retrieving file 'core.db' from mirror.archlinuxarm.org : Could not resolve host: mirror.archlinuxarm.org"}, pkgmgr.KindDNS},
		{[]string{"Setting up libssl3t64:arm64 (3.5.0-1) ...", "Setting up tls-utils (1.0) ...", "E: Sub-process /usr/bin/dpkg returned an error code (1)"}, pkgmgr.KindOther},
	} {
		f := pkgmgr.Diagnose(c.lines, exit)
		if f.Kind != c.kind {
			t.Errorf("%q: kind %s, want %s", c.lines[len(c.lines)-1], f.Kind, c.kind)
		}
		if f.Detail == "" || len(f.Detail) > 120 || regexp.MustCompile(`[^A-Za-z0-9 ._:,+/()-]`).MatchString(f.Detail) {
			t.Errorf("detail %q isn't clean", f.Detail)
		}
	}
	if f := pkgmgr.Diagnose(nil, errors.New("signal: killed")); f.Kind != pkgmgr.KindKilled {
		t.Errorf("killed: %s", f.Kind)
	}
}

// No URLs, addresses or paths go to telemetry; hosts stay.
func TestCleanDetail(t *testing.T) {
	got := pkgmgr.CleanDetail("E: Failed to fetch http://ports.ubuntu.com/ubuntu-ports/pool/main/x/x.deb  404  Not Found [IP: 185.125.190.39 80] in /data/data/com.termux/files/home/x")
	if got != "E: Failed to fetch ports.ubuntu.com 404 Not Found in (path)" {
		t.Errorf("got %q", got)
	}
}

// The fallback walks each group's mirrors in order, and an install that
// switched before goes on from where it is.
func TestMirrorFallback(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc/apt/sources.list.d"), 0o755)
	os.WriteFile(filepath.Join(root, "etc/apt/sources.list.d/debian.sources"), []byte("URIs: http://deb.debian.org/debian\nURIs: http://deb.debian.org/debian-security\n"), 0o644)
	d, err := conf.LoadDistro("debian")
	if err != nil {
		t.Fatal(err)
	}
	fam, _ := pkgmgr.Get(d.Family)
	lg := NewLog("test")
	defer lg.Close()
	p := newPkgOps(&proot.Target{Rootfs: root}, fam, d, lg)
	if !p.nextMirror(nopRep{}) {
		t.Fatal("no switch")
	}
	b, _ := os.ReadFile(filepath.Join(root, "etc/apt/sources.list.d/debian.sources"))
	if string(b) != "URIs: http://ftp.debian.org/debian\nURIs: http://security.debian.org/debian-security\n" {
		t.Errorf("after one switch:\n%s", b)
	}
	q := newPkgOps(&proot.Target{Rootfs: root}, fam, d, lg)
	q.prepare()
	if q.cur[0] != 1 || q.cur[1] != 1 {
		t.Errorf("a new run starts at %v", q.cur)
	}
	q.nextMirror(nopRep{})
	if q.nextMirror(nopRep{}) {
		t.Error("switched past the last mirror")
	}
	b, _ = os.ReadFile(filepath.Join(root, "etc/apt/sources.list.d/debian.sources"))
	if !strings.Contains(string(b), "tsinghua") {
		t.Errorf("last mirror: %s", b)
	}
}

// Every distro's fallback list: real URLs, at least one mirror each.
func TestMirrorData(t *testing.T) {
	for _, id := range conf.DistroIDs() {
		d, _ := conf.LoadDistro(id)
		for _, g := range d.MirrorFallbacks() {
			for _, u := range g {
				if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
					t.Errorf("%s: %q", id, u)
				}
			}
		}
	}
}

func TestPkgErrClasses(t *testing.T) {
	var ue *ui.UserError
	err := pkgErr("Couldn't install xfce desktop", &pkgmgr.Failure{Kind: pkgmgr.KindDiskFull, Detail: "No space left on device", Err: errors.New("exit status 100")})
	if !errors.As(err, &ue) || ue.Class != "not_enough_space" || ue.Kind != "disk_full" {
		t.Errorf("disk full: %+v", err)
	}
	err = pkgErr("Couldn't reach the package servers", &pkgmgr.Failure{Kind: pkgmgr.KindDNS, Detail: "Temporary failure resolving ports.ubuntu.com", Err: errors.New("exit status 100")})
	if !errors.As(err, &ue) || ue.Class != "package_manager" || ue.Kind != "network_dns" || ue.Detail == "" {
		t.Errorf("dns: %+v", err)
	}
}

type nopRep struct{}

func (nopRep) Progress(cur, total int64, unit string) {}
func (nopRep) Line(s string)                          {}
func (nopRep) Detail(s string)                        {}
func (nopRep) Label(s string)                         {}

func TestSpaceNeed(t *testing.T) {
	deb, _ := conf.LoadDistro("debian")
	xfce, _ := conf.ResolveDesktop("xfce")
	kde, _ := conf.ResolveDesktop("kde")
	none, _ := conf.ResolveDesktop("none")
	if n := spaceNeed(deb, xfce, false); n != int64(float64(deb.XFCEMB())*1.2) {
		t.Errorf("debian xfce: %d", n)
	}
	if k, x := spaceNeed(deb, kde, false), spaceNeed(deb, xfce, false); k <= x {
		t.Errorf("kde %d not above xfce %d", k, x)
	}
	if r, f := spaceNeed(deb, xfce, true), spaceNeed(deb, xfce, false); r >= f {
		t.Errorf("a resume (%d) needs less than a fresh install (%d)", r, f)
	}
	if n := spaceNeed(deb, none, false); n <= 0 || n >= spaceNeed(deb, xfce, false) {
		t.Errorf("none: %d", n)
	}
	for _, id := range conf.DistroIDs() {
		d, _ := conf.LoadDistro(id)
		if d.XFCEMB() == 0 {
			t.Errorf("%s: no DISTRO_XFCE_MB", id)
		}
	}
}

// A refresh that fails for a non-network reason doesn't say the servers
// were unreachable; the keyring case has its own message (support #119).
func TestPkgErrRefreshTitles(t *testing.T) {
	title := func(k pkgmgr.Kind) string {
		var ue *ui.UserError
		errors.As(pkgErr(refreshTitle, &pkgmgr.Failure{Kind: k, Detail: "x", Err: errors.New("exit status 1")}), &ue)
		return ue.Title
	}
	if got := title(pkgmgr.KindDNS); got != "Couldn't reach the package servers" {
		t.Errorf("dns: %q", got)
	}
	if got := title(pkgmgr.KindOther); got != refreshTitle {
		t.Errorf("other: %q", got)
	}
	if got := title(pkgmgr.KindKeyring); got != "The package signing keys couldn't be set up" {
		t.Errorf("keyring: %q", got)
	}
}

// The keyring retry starts gpg-agent in the same run, never through a
// pipe, and stops it at the end (support #119: a daemon on our pipe hung
// the run; one started in another proot session was already gone).
func TestKeyringAgentWrap(t *testing.T) {
	w := keyringAgentWrap("pacman -Syy")
	for _, want := range []string{`T="timeout 20"`, "$T gpg-agent --homedir", "--daemon", "</dev/null >/dev/null 2>&1", "{ pacman -Syy; }", "--kill gpg-agent", "exit $s"} {
		if !strings.Contains(w, want) {
			t.Errorf("wrap lacks %q:\n%s", want, w)
		}
	}
	if out, err := sys.Command("sh", "-n", "-c", w).CombinedOutput(); err != nil {
		t.Errorf("not valid sh: %v %s", err, out)
	}
	if strings.Contains(keyringReset, "--daemon") {
		t.Error("the reset must not start a daemon (its own proot session ends it, and it holds the pipe)")
	}
}
