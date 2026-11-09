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
	p.keyringRefresh = "touch " + marker
	p.sleep = func(time.Duration) {}
	pgp := `echo ":: File /var/cache/pacman/pkg/glibc.pkg.tar.xz is corrupted (invalid or corrupted package (PGP signature))."; echo "error: failed to commit transaction (invalid or corrupted package)"; exit 1`
	if err := p.do(context.Background(), "[ -e "+marker+" ] && exit 0; "+pgp, 0, nopRep{}, false); err != nil {
		t.Fatalf("didn't recover after the keyring refresh: %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Error("the keyring wasn't refreshed")
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
