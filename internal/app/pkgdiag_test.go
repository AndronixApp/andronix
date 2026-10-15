package app

import (
	"context"
	"errors"
	"github.com/AndronixApp/andronix-distros/internal/netx"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

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
	// Support #158: Ubuntu 24.04 with MATE peaked at 1579 MB (a16).
	u24, _ := conf.LoadDistro("ubuntu24")
	mate, _ := conf.ResolveDesktop("mate")
	if n := spaceNeed(u24, mate, false); n < 1579 {
		t.Errorf("ubuntu24 mate: %d MB, below the measured peak", n)
	}
	// The other measured peaks (a16, Oct 7), with at least 10% to spare.
	lxqt, _ := conf.ResolveDesktop("lxqt")
	for _, c := range []struct {
		d    *conf.Distro
		de   *conf.Desktop
		peak int64
	}{{deb, xfce, 1179}, {u24, xfce, 1072}, {u24, lxqt, 1374}, {deb, kde, 2194}, {u24, mate, 1579}} {
		if n := spaceNeed(c.d, c.de, false); n < c.peak*11/10 {
			t.Errorf("%s %s: %d MB, under 1.1x the measured peak %d", c.d.ID, c.de.ID, n, c.peak)
		}
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

// Download failures say why (telemetry error_kind), with no URL or path in
// the detail.
func TestDownloadKind(t *testing.T) {
	for _, c := range []struct {
		err  error
		want string
	}{
		{&netx.StatusError{Code: 404}, "http_404"},
		{&net.DNSError{Err: "no such host", Name: "dl.andronix.app"}, "network_dns"},
		{errors.New("read tcp 10.0.0.2:5555->1.2.3.4:443: read: connection reset by peer"), "network_connect"},
		{errors.New("unexpected EOF"), "interrupted"},
		{errors.New("context deadline exceeded (Client.Timeout exceeded while awaiting headers)"), "timeout"},
		{errors.New("something else"), "other"},
	} {
		if got := downloadKind(c.err); got != c.want {
			t.Errorf("%v: %s, want %s", c.err, got, c.want)
		}
	}
	e := downloadErr(errors.New(`Get "https://dl.andronix.app/rootfs/ubuntu/26.04/ubuntu-26.04-arm.tar.xz": unexpected EOF`)).(*ui.UserError)
	if e.Kind != "interrupted" || strings.Contains(e.Detail, "rootfs/") || e.Detail == "" {
		t.Errorf("kind %q detail %q", e.Kind, e.Detail)
	}
}

// A redirect loop or a missing Release file (support #209-211: Kali's
// kali.download and http.kali.org redirect to each other) moves to the next,
// direct mirror once; if that fails too, the message names the network.
func TestMirrorUnusable(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "etc/apt/sources.list.d/kali.sources")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.WriteFile(src, []byte("URIs: http://http.kali.org/kali/\n"), 0o644)
	d, err := conf.LoadDistro("kali")
	if err != nil {
		t.Fatal(err)
	}
	fam, _ := pkgmgr.Get(d.Family)
	lg := NewLog("test")
	defer lg.Close()
	p := newPkgOps(&proot.Target{Rootfs: root}, fam, d, lg)
	f := &pkgmgr.Failure{Kind: pkgmgr.KindMirror, Detail: "Redirection loop encountered", Err: errors.New("exit status 100")}
	if !p.recover(context.Background(), f, 1, true, nopRep{}) {
		t.Fatal("no retry on the first mirror_unusable")
	}
	if b, _ := os.ReadFile(src); string(b) != "URIs: http://mirrors.ocf.berkeley.edu/kali/\n" {
		t.Errorf("after the switch: %s", b)
	}
	if p.recover(context.Background(), f, 2, true, nopRep{}) {
		t.Error("retried a second time")
	}
	// An install an older version rewrote to kali.download goes to the redirector.
	os.WriteFile(src, []byte("URIs: http://kali.download/kali/\n"), 0o644)
	q := newPkgOps(&proot.Target{Rootfs: root}, fam, d, lg)
	if !q.recover(context.Background(), f, 1, true, nopRep{}) {
		t.Fatal("no retry from kali.download")
	}
	if b, _ := os.ReadFile(src); string(b) != "URIs: http://http.kali.org/kali/\n" {
		t.Errorf("from kali.download: %s", b)
	}
	var ue *ui.UserError
	if !errors.As(pkgErr(refreshTitle, f), &ue) || ue.Kind != "mirror_unusable" || !strings.Contains(ue.What, "network") || ue.Title != "The package mirrors sent a broken answer" {
		t.Errorf("message: %+v", ue)
	}
}

// The install ends with how to start it again (support #164 #176 #218).
func TestStartLine(t *testing.T) {
	k, _ := conf.LoadDistro("kali")
	none, _, _ := conf.ResolveDesktopCompat("none")
	xfce, _, _ := conf.ResolveDesktopCompat("xfce")
	if got := startLine(k, none); !strings.Contains(got, "Start Kali Linux any time, from Termux: andronix start kali") {
		t.Errorf("command line: %q", got)
	}
	if got := startLine(k, xfce); !strings.Contains(got, "andronix desktop kali") {
		t.Errorf("desktop: %q", got)
	}
}

// support #312 (a program that isn't root under proot -0) and #471
// (getcwd ENOSYS, gpgv dying: every InRelease "not signed") get one retry
// with PROOT_NO_SECCOMP=1, and the outcome is logged and kept for telemetry
// (<kind>_retried / <kind>_not_fixed).
func TestSeccompRetry(t *testing.T) {
	gpgv := `echo "sh: 0: getcwd() failed: Function not implemented"; echo "Err:1 http://ports.ubuntu.com/ubuntu-ports resolute InRelease"; echo "  Sub-process /usr/bin/gpgv exited unexpectedly"; echo "E: The repository 'http://ports.ubuntu.com/ubuntu-ports resolute InRelease' is not signed."; exit 100`
	for _, c := range []struct {
		name, distro string
		script       string // run in this test's shell: fails once, or always
		wantErr      bool
		want         string // pmSaved, or the failure kind
	}{
		{"fake root fixed", "arch", `if [ -e "$M" ]; then exit 0; fi; touch "$M"; echo "==> ERROR: pacman-key needs to be run as root for this operation."; exit 1`, false, "fake_root_retried"},
		{"fake root not fixed", "arch", `echo "==> ERROR: pacman-key needs to be run as root for this operation."; exit 1`, true, "fake_root_not_fixed"},
		{"gpgv fixed", "ubuntu", `if [ -e "$M" ]; then exit 0; fi; touch "$M"; ` + gpgv, false, "proot_syscall_retried"},
		{"gpgv not fixed", "ubuntu", gpgv, true, "proot_syscall_not_fixed"},
	} {
		d, err := conf.LoadDistro(c.distro)
		if err != nil {
			t.Fatal(err)
		}
		fam, _ := pkgmgr.Get(d.Family)
		proot.NoSeccomp, pmSaved = false, ""
		lg := NewLog("test")
		p := newPkgOps(&proot.Target{Rootfs: t.TempDir(), Local: true}, fam, d, lg)
		p.sleep = func(time.Duration) {}
		t.Setenv("M", filepath.Join(t.TempDir(), "marker"))
		err = p.do(context.Background(), c.script, 0, nopRep{}, false)
		lg.Close()
		b, _ := os.ReadFile(lg.Path)
		if !proot.NoSeccomp {
			t.Errorf("%s: PROOT_NO_SECCOMP not set for the retry", c.name)
		}
		var f *pkgmgr.Failure
		switch {
		case c.wantErr && (!errors.As(err, &f) || string(f.Kind) != c.want):
			t.Errorf("%s: err %v, want kind %s", c.name, err, c.want)
		case !c.wantErr && (err != nil || pmSaved != c.want):
			t.Errorf("%s: err %v, pmSaved %q", c.name, err, pmSaved)
		}
		if !strings.Contains(string(b), "the retry with PROOT_NO_SECCOMP=1") {
			t.Errorf("%s: outcome not logged:\n%s", c.name, b)
		}
		if strings.Count(string(b), "trying again with PROOT_NO_SECCOMP=1") != 1 {
			t.Errorf("%s: retried more than once:\n%s", c.name, b)
		}
	}
	proot.NoSeccomp, pmSaved = false, ""
}
