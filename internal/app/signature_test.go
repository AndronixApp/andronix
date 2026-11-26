package app

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

func setClock(t *testing.T, at time.Time) {
	old := clockNow
	clockNow = func() time.Time { return at }
	t.Cleanup(func() { clockNow = old })
}

// #354/#283: a pacman package that still fails its PGP signature check gets
// the keyring refresh (DISTRO_PRE_UPGRADE) once more before the retry.
func TestPacmanPGPRefreshesKeyring(t *testing.T) {
	setClock(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	d, err := conf.LoadDistro("arch")
	if err != nil {
		t.Fatal(err)
	}
	famReal, _ := pkgmgr.Get(d.Family)
	fam := *famReal
	dir := t.TempDir()
	marker := filepath.Join(dir, "keyring-refreshed")
	fam.Update = "true"
	lg := NewLog("test")
	defer lg.Close()
	p := newPkgOps(&proot.Target{Rootfs: t.TempDir(), Local: true}, &fam, d, lg)
	if !strings.Contains(p.keyringRefresh, "pacman-key --populate") {
		t.Fatalf("pacman pkgOps has no keyring refresh: %q", p.keyringRefresh)
	}
	// Support #5349: the keyring is rebuilt first (pacman-key --init and
	// --populate, no keyserver), then the keyring packages are refreshed.
	if p.keyringRebuild != pkgmgr.PacmanKeyringRebuild {
		t.Fatalf("pacman pkgOps keyring rebuild = %q", p.keyringRebuild)
	}
	rebuilt := filepath.Join(dir, "keyring-rebuilt")
	p.keyringRebuild = "touch " + rebuilt
	p.keyringRefresh = "[ -e " + rebuilt + " ] && touch " + marker
	p.sleep = func(time.Duration) {}
	pgp := `echo ":: File /var/cache/pacman/pkg/glibc.pkg.tar.xz is corrupted (invalid or corrupted package (PGP signature))."; echo "error: failed to commit transaction (invalid or corrupted package)"; exit 1`
	if err := p.do(context.Background(), "[ -e "+marker+" ] && exit 0; "+pgp, 0, nopRep{}, false); err != nil {
		t.Fatalf("didn't recover after the keyring refresh: %v", err)
	}
	if _, err := os.Stat(rebuilt); err != nil {
		t.Error("the keyring wasn't rebuilt")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("the keyring packages weren't refreshed after the rebuild")
	}
}

// Manjaro and Arch share the keyring rebuild; the apt distros have none.
func TestKeyringRebuildOnlyPacman(t *testing.T) {
	lg := NewLog("test")
	defer lg.Close()
	for id, want := range map[string]string{"arch": pkgmgr.PacmanKeyringRebuild, "manjaro": pkgmgr.PacmanKeyringRebuild, "debian": "", "alpine": ""} {
		d, err := conf.LoadDistro(id)
		if err != nil {
			t.Fatal(err)
		}
		fam, _ := pkgmgr.Get(d.Family)
		if p := newPkgOps(&proot.Target{Rootfs: t.TempDir(), Local: true}, fam, d, lg); p.keyringRebuild != want {
			t.Errorf("%s: keyring rebuild = %q, want %q", id, p.keyringRebuild, want)
		}
	}
}

func pacmanOps(t *testing.T) (*pkgOps, *pkgmgr.Family) {
	t.Helper()
	d, err := conf.LoadDistro("arch")
	if err != nil {
		t.Fatal(err)
	}
	famReal, _ := pkgmgr.Get(d.Family)
	fam := *famReal
	lg := NewLog("test")
	t.Cleanup(func() { lg.Close() })
	p := newPkgOps(&proot.Target{Rootfs: t.TempDir(), Local: true}, &fam, d, lg)
	p.sleep = func(time.Duration) {}
	return p, &fam
}

// Support #5412: pacman stopping on a half-installed package (killed while
// unpacking it in an earlier run) gets the repair once, then the retry.
func TestPacmanDBDamageRepaired(t *testing.T) {
	setClock(t, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	p, fam := pacmanOps(t)
	dir := t.TempDir()
	repaired, runs := filepath.Join(dir, "repaired"), filepath.Join(dir, "runs")
	fam.Repair = "touch " + repaired
	fam.Update = "exit 9" // a refresh here would be the hash_mismatch path, not this one
	damaged := `echo x >>` + runs + `; [ -e ` + repaired + ` ] && exit 0; echo "error: could not open file /var/lib/pacman/local/glib2-2.90.1-1/desc: No such file or directory"; echo "warning: could not fully load metadata for package glib2-2.90.1-1"; echo "error: failed to prepare transaction (invalid or corrupted package)"; exit 1`
	if err := p.do(context.Background(), damaged, 0, nopRep{}, false); err != nil {
		t.Fatalf("didn't recover after the repair: %v", err)
	}
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "x") != 2 {
		t.Errorf("ran %d times, want 2 (fail, repair, pass)", strings.Count(string(b), "x"))
	}

	// A repair that doesn't help stops after one try, as pacman_db.
	p, fam = pacmanOps(t)
	fam.Repair = "true"
	os.Remove(runs)
	os.Remove(repaired)
	err := p.do(context.Background(), damaged, 0, nopRep{}, false)
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindPacmanDB {
		t.Fatalf("got %v, want a %s failure", err, pkgmgr.KindPacmanDB)
	}
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "x") != 2 {
		t.Errorf("ran %d times, want 2 (no endless repair)", strings.Count(string(b), "x"))
	}
	if ue, ok := pkgErr("Couldn't update Arch Linux", err).(*ui.UserError); !ok || ue.Kind != "pacman_db" || ue.Title != "An earlier install was stopped half-way" {
		t.Errorf("user error = %+v", pkgErr("Couldn't update Arch Linux", err))
	}
}

// The refresh is followed by the repair, and a failed refresh skips it.
func TestUpdateRunsRepair(t *testing.T) {
	p, fam := pacmanOps(t)
	dir := t.TempDir()
	order := filepath.Join(dir, "order")
	fam.Update = "echo update >>" + order
	fam.Repair = "echo repair >>" + order
	if err := p.update(context.Background(), nopRep{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(order); string(b) != "update\nrepair\n" {
		t.Errorf("order = %q, want update then repair", b)
	}
	os.Remove(order)
	fam.Update = "echo 'error: target not found: nope'; exit 1"
	if err := p.update(context.Background(), nopRep{}); err == nil {
		t.Fatal("a failed refresh passed")
	}
	if _, err := os.Stat(order); err == nil {
		t.Error("the repair ran after a failed refresh")
	}
}

// The repair script itself, against a made-up local database and a pacman
// that only records its arguments: which entries count as half-installed,
// how their names are read (epochs, dashes in names), what is asked of
// pacman, and that a failed reinstall is tried again next time.
func TestPacmanRepairScript(t *testing.T) {
	root := t.TempDir()
	local := filepath.Join(root, "var/lib/pacman/local")
	entry := func(name string, files ...string) {
		os.MkdirAll(filepath.Join(local, name), 0o755)
		for _, f := range files {
			os.WriteFile(filepath.Join(local, name, f), []byte("%NAME%\n"), 0o644)
		}
	}
	entry("bash-5.3.20-1", "desc", "files", "mtree")
	entry("glib2-2.90.1-1", "mtree")                                         // killed while unpacking
	entry("nftables-1:1.1.7-3")                                              // epoch, empty
	entry("xorg-server-xvfb-21.1.18-2", "desc", "mtree")                     // no files
	entry("gone-from-repos-1.0-1", "mtree")                                  // not in the sync db any more
	os.WriteFile(filepath.Join(local, "glib2-2.90.1-1", "desc"), nil, 0o644) // empty desc counts as missing
	os.WriteFile(filepath.Join(local, "ALPM_DB_VERSION"), []byte("9\n"), 0o644)
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	calls := filepath.Join(root, "calls")
	fake := "#!/bin/sh\ncase \"$*\" in\n  '-Si gone-from-repos') exit 1;;\n  -Si*) exit 0;;\nesac\necho \"$*\" >>" + calls + "\n[ ! -e " + root + "/fail ]\n"
	os.WriteFile(filepath.Join(bin, "pacman"), []byte(fake), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := strings.ReplaceAll(pkgmgr.PacmanRepair, "/var/lib/pacman", filepath.Join(root, "var/lib/pacman"))
	if script == pkgmgr.PacmanRepair {
		t.Fatal("the script doesn't use /var/lib/pacman")
	}
	// The process list is made up too (a pacman of the user's own).
	proc := filepath.Join(root, "proc")
	script = strings.ReplaceAll(script, "/proc/", proc+"/")
	tg := &proot.Target{Rootfs: root, Local: true}
	todo := filepath.Join(root, "var/lib/pacman/andronix-reinstall")
	const want = "--noconfirm --noprogressbar --color never -S --overwrite * glib2 nftables xorg-server-xvfb\n"

	// 0: a pacman is running (maybe mid-unpack): nothing is touched.
	os.MkdirAll(filepath.Join(proc, "4242"), 0o755)
	os.WriteFile(filepath.Join(proc, "4242", "comm"), []byte("pacman\n"), 0o644)
	if err := tg.Run(context.Background(), script, nil, nil); err != nil {
		t.Errorf("with pacman running: %v", err)
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("pacman was called while one runs: %q", b)
	}
	if _, err := os.Stat(filepath.Join(local, "glib2-2.90.1-1")); err != nil {
		t.Error("an entry was removed while pacman runs")
	}
	os.RemoveAll(proc)

	// 1: the reinstall fails (no network): the names are kept.
	os.WriteFile(filepath.Join(root, "fail"), nil, 0o644)
	if err := tg.Run(context.Background(), script, nil, nil); err == nil {
		t.Error("a failed reinstall passed")
	}
	if b, _ := os.ReadFile(calls); string(b) != want {
		t.Errorf("pacman got %q, want %q", b, want)
	}
	if b, _ := os.ReadFile(todo); !strings.Contains(string(b), "glib2\n") {
		t.Errorf("the names weren't kept for the next run: %q", b)
	}
	for _, gone := range []string{"glib2-2.90.1-1", "nftables-1:1.1.7-3", "xorg-server-xvfb-21.1.18-2", "gone-from-repos-1.0-1"} {
		if _, err := os.Stat(filepath.Join(local, gone)); err == nil {
			t.Errorf("the half-made entry %s is still there", gone)
		}
	}
	if _, err := os.Stat(filepath.Join(local, "bash-5.3.20-1", "desc")); err != nil {
		t.Error("a whole entry was removed")
	}

	// 2: next run, nothing is damaged any more but the list is: reinstall.
	os.Remove(filepath.Join(root, "fail"))
	os.Remove(calls)
	if err := tg.Run(context.Background(), script, nil, nil); err != nil {
		t.Errorf("the second run failed: %v", err)
	}
	if b, _ := os.ReadFile(calls); string(b) != want {
		t.Errorf("second run: pacman got %q, want %q", b, want)
	}
	if _, err := os.Stat(todo); err == nil {
		t.Error("the list wasn't cleared after the reinstall")
	}

	// 3: a healthy database: pacman isn't run at all.
	os.Remove(calls)
	if err := tg.Run(context.Background(), script, nil, nil); err != nil {
		t.Errorf("a healthy database failed: %v", err)
	}
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("pacman ran on a healthy database: %q", b)
	}
}

// #354: a signature failure on a phone whose clock is far behind stops at
// once as clock_skew (no mirror hopping), and the message names the date.
func TestSignatureFailureWrongClock(t *testing.T) {
	setClock(t, time.Date(2016, 1, 1, 0, 0, 0, 0, time.UTC))
	root := t.TempDir()
	src := filepath.Join(root, "etc/apt/sources.list.d/kali.sources")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.WriteFile(src, []byte("URIs: http://http.kali.org/kali/\n"), 0o644)
	// Lists from the image exist, so the "going on with the lists apt has"
	// shortcut would otherwise hide the failure until "unauthenticated".
	os.MkdirAll(filepath.Join(root, "var/lib/apt/lists"), 0o755)
	os.WriteFile(filepath.Join(root, "var/lib/apt/lists/http.kali.org_kali_dists_kali-rolling_main_binary-arm64_Packages"), []byte("Package: x\n"), 0o644)
	d, err := conf.LoadDistro("kali")
	if err != nil {
		t.Fatal(err)
	}
	fam, _ := pkgmgr.Get(d.Family)
	lg := NewLog("test")
	defer lg.Close()
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, fam, d, lg)
	p.sleep = func(time.Duration) {}
	runs := filepath.Join(t.TempDir(), "runs")
	notSigned := `echo x >> ` + runs + `; echo "E: The repository 'http://http.kali.org/kali kali-rolling InRelease' is not signed."; exit 100`
	err = p.do(context.Background(), notSigned, 0, nopRep{}, true)
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindClock || !strings.Contains(f.Detail, "2016") {
		t.Fatalf("want clock_skew naming the date, got %v", err)
	}
	if p.switched {
		t.Error("switched mirrors for a wrong clock")
	}
	if b, _ := os.ReadFile(runs); strings.Count(string(b), "x") != 1 {
		t.Errorf("ran %d times, want 1", strings.Count(string(b), "x"))
	}
	var ue *ui.UserError
	if !errors.As(pkgErr(refreshTitle, err), &ue) || ue.Class != "clock_wrong" || !strings.Contains(ue.What, "2016") {
		t.Errorf("message: %+v", ue)
	}

	// With a right clock the same failure keeps its mirror fallback.
	setClock(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	f2 := pkgmgr.Diagnose([]string{"E: The repository 'x InRelease' is not signed."}, errors.New("exit status 100"))
	if f2.Kind != pkgmgr.KindIntercepted {
		t.Fatalf("kind %s", f2.Kind)
	}
	var ue2 *ui.UserError
	if errors.As(pkgErr(refreshTitle, f2), &ue2); ue2.Class == "clock_wrong" {
		t.Error("clock message with a right clock")
	}
}

// #354 (#4213): "unauthenticated packages" refreshes the lists once; a refresh
// that fails too stops with a clear message.
func TestUnauthenticatedRefreshes(t *testing.T) {
	setClock(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	d, _ := conf.LoadDistro("debian")
	famReal, _ := pkgmgr.Get("apt")
	unauth := `echo "WARNING: The following packages cannot be authenticated!"; echo "E: There were unauthenticated packages and -y was used without --allow-unauthenticated"; exit 100`
	t.Run("refresh fixes it", func(t *testing.T) {
		fam := *famReal
		marker := filepath.Join(t.TempDir(), "refreshed")
		fam.Update = "touch " + marker
		lg := NewLog("test")
		defer lg.Close()
		p := newPkgOps(&proot.Target{Rootfs: t.TempDir(), Local: true}, &fam, d, lg)
		p.sleep = func(time.Duration) {}
		if err := p.do(context.Background(), "[ -e "+marker+" ] && exit 0; "+unauth, 0, nopRep{}, false); err != nil {
			t.Fatalf("not recovered: %v", err)
		}
	})
	t.Run("refresh fails", func(t *testing.T) {
		fam := *famReal
		fam.Update = "exit 100"
		lg := NewLog("test")
		defer lg.Close()
		p := newPkgOps(&proot.Target{Rootfs: t.TempDir(), Local: true}, &fam, d, lg)
		p.sleep = func(time.Duration) {}
		err := p.do(context.Background(), unauth, 0, nopRep{}, false)
		var ue *ui.UserError
		if !errors.As(pkgErr("Couldn't update Debian", err), &ue) || ue.Kind != "unauthenticated" || ue.Title != "The package lists couldn't be verified" {
			t.Fatalf("got %+v", ue)
		}
	})
}

// #377: apt that can't load its own libraries (32-bit, kernel 3.18) stops at
// once with its own class, not "other" after five tries.
func TestGuestLibsNoRetry(t *testing.T) {
	d, _ := conf.LoadDistro("ubuntu24")
	fam, _ := pkgmgr.Get("apt")
	lg := NewLog("test")
	defer lg.Close()
	p := newPkgOps(&proot.Target{Rootfs: t.TempDir(), Local: true}, fam, d, lg)
	p.sleep = func(time.Duration) {}
	runs := filepath.Join(t.TempDir(), "runs")
	libs := "echo x >> " + runs + "; echo \"apt-get: /lib/arm-linux-gnueabihf/libapt-private.so.0.0: version \\`GLIBCXX_3.4.21' not found (required by apt-get)\"; exit 1"
	err := p.do(context.Background(), libs, 0, nopRep{}, true)
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindGuestLibs {
		t.Fatalf("got %v", err)
	}
	if b, _ := os.ReadFile(runs); len(b) != 2 {
		t.Errorf("ran %d times, want 1", len(b)/2)
	}
	var ue *ui.UserError
	if !errors.As(pkgErr(refreshTitle, err), &ue) || ue.Class != "guest_libs" || !strings.Contains(ue.Fix, "report") {
		t.Errorf("message: %+v", ue)
	}
}

// A phone dated 2016 on the normal download path: every server certificate
// is "not yet valid". That's the clock, not the network (2.1.5-rc1 gate).
func TestClockCertErr(t *testing.T) {
	certErr := errors.New(`Get "https://auth.docker.io/token?service=registry.docker.io": tls: failed to verify certificate: x509: certificate has expired or is not yet valid: current time 2016-05-01T00:00:19Z is before 2026-11-03T01:10:10Z`)
	typed := fmt.Errorf("get: %w", x509.CertificateInvalidError{Reason: x509.Expired})

	setClock(t, time.Date(2016, 5, 1, 0, 0, 19, 0, time.UTC))
	for _, err := range []error{certErr, typed} {
		ue, ok := clockCertErr(err)
		if !ok || ue.Class != "clock_wrong" || ue.Title != "Your phone's date is wrong" || !strings.Contains(ue.What, "1 May 2016") {
			t.Fatalf("clockCertErr(%v) = %+v, %v; want clock_wrong naming 1 May 2016", err, ue, ok)
		}
		var got *ui.UserError
		if !errors.As(downloadErr(err, false), &got) || got.Class != "clock_wrong" {
			t.Fatalf("downloadErr(%v) = %+v; want clock_wrong", err, got)
		}
	}
	if _, ok := clockCertErr(errors.New("dial tcp: connection refused")); ok {
		t.Fatal("a network error is not a clock error")
	}
	if _, ok := clockCertErr(x509.UnknownAuthorityError{}); ok {
		t.Fatal("an unknown CA (an intercepting proxy) is not a clock error")
	}

	// The right date: an expired certificate is the server's problem.
	setClock(t, time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC))
	if _, ok := clockCertErr(certErr); ok {
		t.Fatal("with the date right, an expired certificate is not a clock error")
	}
	var got *ui.UserError
	if !errors.As(downloadErr(certErr, false), &got) || got.Class != "download" {
		t.Fatalf("downloadErr with the right date = %+v; want the plain download error", got)
	}
}
