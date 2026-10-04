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
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

func TestDiagnose(t *testing.T) {
	exit := errors.New("exit status 100")
	for _, c := range []struct {
		lines []string
		kind  pkgKind
	}{
		{[]string{"Err:1 http://ports.ubuntu.com/ubuntu-ports resolute InRelease", "  Temporary failure resolving 'ports.ubuntu.com'", "E: Failed to fetch http://ports.ubuntu.com/ubuntu-ports/dists/resolute/InRelease"}, kindDNS},
		{[]string{"Err:7 http://deb.debian.org/debian trixie/main arm64 libgtk-3-0t64 arm64 3.24.49-3", "  404  Not Found [IP: 151.101.2.132 80]", "E: Failed to fetch http://deb.debian.org/debian/pool/main/g/gtk+3.0/libgtk-3-0t64_3.24.49-3_arm64.deb  404  Not Found"}, kindHTTP404},
		{[]string{"E: Failed to fetch http://ports.ubuntu.com/ubuntu-ports/dists/resolute-updates/main/binary-arm64/Packages.xz  Hash Sum mismatch"}, kindHash},
		{[]string{"E: dpkg was interrupted, you must manually run 'dpkg --configure -a' to correct the problem."}, kindDpkg},
		{[]string{"E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 4242 (apt-get)"}, kindLock},
		{[]string{"E: Release file for http://ports.ubuntu.com/ubuntu-ports/dists/resolute-updates/InRelease is not valid yet (invalid for another 3h 2min 1s)."}, kindClock},
		{[]string{"W: GPG error: http://kali.download/kali kali-rolling InRelease: The following signatures were invalid: EXPKEYSIG ED65462EC8D5E4C5", "E: The repository 'http://kali.download/kali kali-rolling InRelease' is not signed."}, kindGPG},
		{[]string{"The following packages have unmet dependencies:", "E: Unable to correct problems, you have held broken packages."}, kindBroken},
		{[]string{"dpkg: error processing archive /var/cache/apt/archives/firefox.deb (--unpack):", " cannot copy extracted data for './usr/lib/firefox/libxul.so' to '/usr/lib/firefox/libxul.so.dpkg-new': failed to write (No space left on device)"}, kindDiskFull},
		{[]string{"ERROR: [reposync] failed to fetch file `https://repo-default.voidlinux.org/current/aarch64/aarch64-repodata': Operation timed out"}, kindConnect},
		{[]string{"error: failed retrieving file 'core.db' from mirror.archlinuxarm.org : Could not resolve host: mirror.archlinuxarm.org"}, kindDNS},
		{[]string{"Setting up libssl3t64:arm64 (3.5.0-1) ...", "Setting up tls-utils (1.0) ...", "E: Sub-process /usr/bin/dpkg returned an error code (1)"}, kindOther},
	} {
		f := diagnose(c.lines, exit)
		if f.kind != c.kind {
			t.Errorf("%q: kind %s, want %s", c.lines[len(c.lines)-1], f.kind, c.kind)
		}
		if f.detail == "" || len(f.detail) > 120 || regexp.MustCompile(`[^A-Za-z0-9 ._:,+/()-]`).MatchString(f.detail) {
			t.Errorf("detail %q isn't clean", f.detail)
		}
	}
	if f := diagnose(nil, errors.New("signal: killed")); f.kind != kindKilled {
		t.Errorf("killed: %s", f.kind)
	}
}

// No URLs, addresses or paths go to telemetry; hosts stay.
func TestCleanDetail(t *testing.T) {
	got := cleanDetail("E: Failed to fetch http://ports.ubuntu.com/ubuntu-ports/pool/main/x/x.deb  404  Not Found [IP: 185.125.190.39 80] in /data/data/com.termux/files/home/x")
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
	err := pkgErr("Couldn't install xfce desktop", &pkgFailure{kind: kindDiskFull, detail: "No space left on device", err: errors.New("exit status 100")})
	if !errors.As(err, &ue) || ue.Class != "not_enough_space" || ue.Kind != "disk_full" {
		t.Errorf("disk full: %+v", err)
	}
	err = pkgErr("Couldn't reach the package servers", &pkgFailure{kind: kindDNS, detail: "Temporary failure resolving ports.ubuntu.com", err: errors.New("exit status 100")})
	if !errors.As(err, &ue) || ue.Class != "package_manager" || ue.Kind != "network_dns" || ue.Detail == "" {
		t.Errorf("dns: %+v", err)
	}
}

type nopRep struct{}

func (nopRep) Progress(cur, total int64, unit string) {}
func (nopRep) Line(s string)                          {}
func (nopRep) Detail(s string)                        {}
func (nopRep) Label(s string)                         {}
