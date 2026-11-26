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

// The 2.1.6-rc2 device gates on the Redmi (Nov 13 2026), bugs 3 to 5.

const debianSources = "Types: deb\nURIs: http://deb.debian.org/debian\nSuites: trixie trixie-updates\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.pgp\n\n" +
	"Types: deb\nURIs: http://deb.debian.org/debian-security\nSuites: trixie-security\nComponents: main\nSigned-By: /usr/share/keyrings/debian-archive-keyring.pgp\n"

// debianRoot is a rootfs with our Debian 13 image's sources and no lists.
func debianRoot(t *testing.T) (root, src string) {
	t.Helper()
	root = t.TempDir()
	src = filepath.Join(root, "etc/apt/sources.list.d/debian.sources")
	os.MkdirAll(filepath.Dir(src), 0o755)
	os.WriteFile(src, []byte(debianSources), 0o644)
	return root, src
}

// refuseMirrors makes the check before a mirror switch refuse every mirror.
func refuseMirrors(t *testing.T) {
	t.Helper()
	old := mirrorProbe
	mirrorProbe = func(context.Context, string, string, string) string {
		return "trixie: no Release file (connection refused)"
	}
	t.Cleanup(func() { mirrorProbe = old })
}

// debianRefresh is apt-get update as apt 3.0.3 runs it in a Debian 13 whose
// main archive doesn't answer (base: what the sources name it) while the
// security archive does: warnings, the security lists, then the
// ca-certificates install from them, and exit status 0. Once the sources
// don't use base any more the main archive's lists arrive too. Every run
// adds a line to calls.
func debianRefresh(root, src, base, calls string) string {
	lists := filepath.Join(root, "var/lib/apt/lists")
	sec := filepath.Join(lists, "deb.debian.org_debian-security_dists_trixie-security_")
	return `echo run >>` + calls + `; mkdir -p ` + lists + `
printf 'Origin: Debian\nDate: Thu, 12 Nov 2026 20:47:45 UTC\n' >` + sec + `InRelease; : >` + sec + `main_binary-arm64_Packages.lz4
if grep -q "` + base + `" ` + src + `; then
echo "Err:1 ` + strings.TrimSuffix(base, "/") + ` trixie InRelease"
echo "  Could not connect to 127.0.0.1:9 (127.0.0.1). - connect (111: Connection refused)"
echo "Reading package lists..."
echo "W: Failed to fetch ` + base + `dists/trixie/InRelease  Unable to connect to 127.0.0.1:9:"
echo "W: Failed to fetch ` + base + `dists/trixie-updates/InRelease  Unable to connect to 127.0.0.1:9:"
echo "W: Some index files failed to download. They have been ignored, or old ones used instead."
i=0; while [ $i -lt 90 ]; do echo "Setting up ca-certificates ($i)"; i=$((i+1)); done
exit 0
fi
` + listsFor(root, "http://deb.debian.org/debian", "trixie")
}

// Bug 3: Debian with ANDRONIX_TEST_BREAK_MIRROR=1 never fell back. Its
// security source is another server, which still answered, so apt-get update
// exited 0 with warnings, the ca-certificates install after it worked from
// the security lists, and the refresh counted as done without one list from
// the main archive: "Package adduser has no installation candidate" two
// steps later. Ubuntu and Kali have one server for everything, so there the
// ca-certificates install failed and the refresh was retried.
func TestDebianRefreshWithoutTheMainArchive(t *testing.T) {
	const dead = "http://127.0.0.1:9/andronix-broken/"
	setup := func(t *testing.T) (p *pkgOps, src, calls string, slept *[]time.Duration, logged func() string) {
		t.Setenv("ANDRONIX_TEST_BREAK_MIRROR", "1")
		d, famReal := loadDistroFam(t, "debian")
		root, src := debianRoot(t)
		calls = filepath.Join(t.TempDir(), "calls")
		fam := *famReal
		fam.Update, fam.Repair = debianRefresh(root, src, dead, calls), ""
		lg, buf := bufLog()
		p = newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, d, lg)
		slept = new([]time.Duration)
		p.sleep = func(d time.Duration) { *slept = append(*slept, d) }
		return p, src, calls, slept, buf.String
	}

	t.Run("falls back like Ubuntu and Kali", func(t *testing.T) {
		p, src, calls, slept, logged := setup(t)
		if err := p.update(context.Background(), nopRep{}); err != nil {
			t.Fatalf("the refresh didn't recover: %v\n%s", err, logged())
		}
		b, _ := os.ReadFile(src)
		// The main archive moved to the distro's own server; the security
		// source answered all along and stays where it was.
		if string(b) != debianSources {
			t.Errorf("sources after the refresh:\n%s\nwant\n%s\n%s", b, debianSources, logged())
		}
		if !reflect.DeepEqual(p.cur, []int{1, 0}) {
			t.Errorf("cur = %v, want the main group moved and the security group not", p.cur)
		}
		if got := len(callsOf(calls)); got != 3 {
			t.Errorf("apt ran %d times, want 3 (two tries on the dead server, one on the next)", got)
		}
		if want := []time.Duration{5 * time.Second}; !reflect.DeepEqual(*slept, want) {
			t.Errorf("pauses %v, want %v", *slept, want)
		}
		for _, s := range []string{"attempt 1 failed: network_connect:", "switched the sources from " + dead + " to http://deb.debian.org/debian", "worked on attempt 3"} {
			if !strings.Contains(logged(), s) {
				t.Errorf("log lacks %q:\n%s", s, logged())
			}
		}
		if p.moveOnly != nil {
			t.Error("the next command's mirror switch is still limited to this refresh's groups")
		}
	})

	// No mirror can be used either: the refresh fails with the network error.
	// It doesn't "go on with the lists apt has": those are the security
	// archive's only.
	t.Run("no mirror answers", func(t *testing.T) {
		refuseMirrors(t)
		p, src, calls, slept, logged := setup(t)
		err := p.update(context.Background(), nopRep{})
		var f *pkgmgr.Failure
		if !errors.As(err, &f) || f.Kind != pkgmgr.KindConnect {
			t.Fatalf("want a connect failure, got %v\n%s", err, logged())
		}
		if got := len(callsOf(calls)); got != 4 {
			t.Errorf("apt ran %d times, want 4", got)
		}
		if want := []time.Duration{5 * time.Second, 20 * time.Second, 30 * time.Second}; !reflect.DeepEqual(*slept, want) {
			t.Errorf("pauses %v, want %v", *slept, want)
		}
		if b, _ := os.ReadFile(src); strings.Count(string(b), dead) != 1 || !strings.Contains(string(b), "URIs: http://deb.debian.org/debian-security\n") {
			t.Errorf("the sources moved:\n%s", b)
		}
		if e := pkgErr(refreshTitle, err).(*ui.UserError); e.Title != "Couldn't reach the package servers" || e.Kind != "network_connect" {
			t.Errorf("user error: %q %q", e.Title, e.Kind)
		}
	})

	// Older lists from the main archive are on disk (an update, a resumed
	// install): apt goes on with them, and so does the refresh, as before.
	t.Run("older lists on disk", func(t *testing.T) {
		p, src, calls, slept, logged := setup(t)
		p.prepare() // the sources point at the dead server now
		before, _ := os.ReadFile(src)
		lists := filepath.Join(p.t.Rootfs, "var/lib/apt/lists")
		os.MkdirAll(lists, 0o755)
		os.WriteFile(filepath.Join(lists, "127.0.0.1:9_andronix-broken_dists_trixie_InRelease"), []byte("Origin: Debian\nDate: Sun, 08 Nov 2026 07:49:44 UTC\n"), 0o644)
		if err := p.update(context.Background(), nopRep{}); err != nil {
			t.Fatalf("%v\n%s", err, logged())
		}
		if after, _ := os.ReadFile(src); string(after) != string(before) || len(callsOf(calls)) != 1 || len(*slept) != 0 {
			t.Errorf("apt ran %d times, pauses %v, sources:\n%s", len(callsOf(calls)), *slept, after)
		}
	})
}

// Bug 3, the other way a refresh ended without the main archive: every try
// fails (here apt 3's "does not have a Release file" for a server it never
// reached, bug 4), no mirror can be used, and the security archive's lists
// are on disk. "Some package sources didn't answer; going on with the
// others" then led to the same "no installation candidate".
func TestRefreshDoesntGoOnWithoutTheMainArchive(t *testing.T) {
	refuseMirrors(t)
	d, famReal := loadDistroFam(t, "debian")
	root, src := debianRoot(t)
	lists := filepath.Join(root, "var/lib/apt/lists")
	os.MkdirAll(lists, 0o755)
	sec := filepath.Join(lists, "deb.debian.org_debian-security_dists_trixie-security_")
	os.WriteFile(sec+"InRelease", []byte("Origin: Debian\nDate: Thu, 12 Nov 2026 20:47:45 UTC\n"), 0o644)
	os.WriteFile(sec+"main_binary-arm64_Packages.lz4", nil, 0o644)
	fam := *famReal
	fam.Repair = ""
	fam.Update = `echo "Err:4 http://deb.debian.org/debian trixie Release"
echo "  Cannot initiate the connection to deb.debian.org:80 (192.0.2.5). - connect (101: Network is unreachable)"
echo "E: The repository 'http://deb.debian.org/debian trixie Release' does not have a Release file."
exit 100`
	lg, logged := bufLog()
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, d, lg)
	p.sleep = func(time.Duration) {}
	err := p.update(context.Background(), nopRep{})
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindConnect {
		t.Fatalf("want the connect failure, got %v\n%s", err, logged)
	}
	if strings.Contains(logged.String(), "going on with the lists apt has") {
		t.Errorf("went on without the main archive's lists:\n%s", logged)
	}
	if b, _ := os.ReadFile(src); string(b) != debianSources {
		t.Errorf("sources changed:\n%s", b)
	}

	// With the main archive's lists on disk too (older ones), it goes on, as before.
	os.WriteFile(filepath.Join(lists, "deb.debian.org_debian_dists_trixie_InRelease"), []byte("Origin: Debian\nDate: Sun, 08 Nov 2026 07:49:44 UTC\n"), 0o644)
	t.Setenv("PATH", t.TempDir()+string(os.PathListSeparator)+os.Getenv("PATH"))
	fakeTool(t, strings.Split(os.Getenv("PATH"), string(os.PathListSeparator))[0], "dpkg", "exit 0")
	lg, logged = bufLog()
	p = newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, d, lg)
	p.sleep = func(time.Duration) {}
	if err := p.update(context.Background(), nopRep{}); err != nil || !strings.Contains(logged.String(), "going on with the lists apt has") {
		t.Errorf("with older lists for every server: %v\n%s", err, logged)
	}
}

// apt 3 with the phone offline, as Ubuntu 26.04 printed it on the Redmi.
const aptOffline = `echo "Ign:1 http://ports.ubuntu.com/ubuntu-ports resolute InRelease"
echo "Err:5 http://ports.ubuntu.com/ubuntu-ports resolute Release"
echo "  Cannot initiate the connection to ports.ubuntu.com:80 (2620:2d:4002:1::10b). - connect (101: Network is unreachable) Cannot initiate the connection to ports.ubuntu.com:80 (91.189.91.104). - connect (101: Network is unreachable)"
echo "Err:6 http://ports.ubuntu.com/ubuntu-ports resolute-updates Release"
echo "  Cannot initiate the connection to ports.ubuntu.com:80 (2620:2d:4002:1::10b). - connect (101: Network is unreachable) Cannot initiate the connection to ports.ubuntu.com:80 (91.189.91.104). - connect (101: Network is unreachable)"
echo "Reading package lists..."
echo "E: The repository 'http://ports.ubuntu.com/ubuntu-ports resolute Release' does not have a Release file."
echo "E: The repository 'http://ports.ubuntu.com/ubuntu-ports resolute-updates Release' does not have a Release file."
exit 100`

// Bug 4: Ubuntu 26.04 offline at the refresh failed in 2 s, after one try,
// with "The package mirrors sent a broken answer". apt 3 words a server it
// can't reach ("Network is unreachable") as "does not have a Release file",
// which read as a mirror without its index. It is the network: the usual
// tries and pauses, and "Couldn't reach the package servers".
func TestAptOfflineIsTheNetworkNotTheMirror(t *testing.T) {
	refuseMirrors(t) // offline: no mirror answers the check either
	primary := "http://ports.ubuntu.com/ubuntu-ports/"
	root := ubuntuRoot(t, primary, time.Time{})
	before := sourcesOf(root)
	calls := filepath.Join(t.TempDir(), "calls")
	famReal, _ := pkgmgr.Get("apt")
	fam := *famReal
	fam.Update, fam.Repair = "echo run >>"+calls+"\n"+aptOffline, ""
	lg, logged := bufLog()
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, nil, lg)
	var slept []time.Duration
	p.sleep = func(d time.Duration) { slept = append(slept, d) }
	p.mirrors, p.cur, p.home = [][]string{{primary, "http://ftp.tu-chemnitz.de/pub/linux/ubuntu-ports/"}}, []int{0}, []int{0}

	err := p.update(context.Background(), nopRep{})
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindConnect {
		t.Fatalf("want a connect failure, got %v\n%s", err, logged)
	}
	if got := len(callsOf(calls)); got != 4 {
		t.Errorf("apt ran %d times, want 4 (as Ubuntu 24.04 offline)", got)
	}
	if want := []time.Duration{5 * time.Second, 20 * time.Second, 30 * time.Second}; !reflect.DeepEqual(slept, want) {
		t.Errorf("pauses %v, want %v", slept, want)
	}
	if sourcesOf(root) != before {
		t.Errorf("sources changed:\n%s", sourcesOf(root))
	}
	e := pkgErr(refreshTitle, err).(*ui.UserError)
	if e.Title != "Couldn't reach the package servers" || e.Kind != "network_connect" || !strings.Contains(e.Fix, "Check your internet") || !strings.Contains(e.What, "Network is unreachable") {
		t.Errorf("user error: %q %q\n%s\n%s", e.Title, e.Kind, e.What, e.Fix)
	}
}

// alpinePack is Alpine as pack add sees it from Termux, with the retry code
// packOps gives it and the pauses recorded.
func alpinePack(t *testing.T, root string) (*packPlace, *[]time.Duration, func() string) {
	t.Helper()
	fam, err := pkgmgr.Get("apk")
	if err != nil {
		t.Fatal(err)
	}
	lg, logged := bufLog()
	pp := &packPlace{id: "alpine", t: &proot.Target{Rootfs: root, Local: true}, fam: fam, label: "Alpine Linux 3.24", cmd: "andronix pack alpine"}
	slept := new([]time.Duration)
	if pp.ops = packOps(pp, lg); pp.ops != nil {
		pp.ops.sleep = func(d time.Duration) { *slept = append(*slept, d) }
	}
	return pp, slept, logged.String
}

// Bug 5: `andronix pack add alpine python` with resolv.conf's DNS servers
// dead made one try, never asked Android for the package servers' addresses,
// and ended with "The package manager stopped with an error.: exit status
// 2". The refresh now runs through the code install and update use.
func TestPackRefreshAlpineDeadDNS(t *testing.T) {
	root := alpineRoot(t)
	hosts := filepath.Join(root, "etc/hosts")
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// apk works once dl-cdn is in /etc/hosts.
	fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; grep -q '^151.101.2.132 dl-cdn.alpinelinux.org ' `+hosts+` && exit 0; `+apkDNSFail)

	t.Run("Android's addresses", func(t *testing.T) {
		lookups := androidAnswers(t, []string{"151.101.2.132", "151.101.66.132"}, nil)
		pp, slept, logged := alpinePack(t, root)
		if pp.ops == nil {
			t.Fatal("Alpine's pack commands don't get the retry code")
		}
		if err := packRefresh(context.Background(), pp, nopRep{}); err != nil {
			t.Fatalf("the refresh didn't recover: %v\n%s", err, logged())
		}
		first := "--timeout 15 --no-progress update"
		if got, want := callsOf(calls), []string{first, first}; !reflect.DeepEqual(got, want) || len(*slept) != 0 || *lookups != 1 {
			t.Errorf("apk ran as %q (pauses %v, %d lookups), want %q at once after one lookup", got, *slept, *lookups, want)
		}
		want := userHosts + "151.101.2.132 dl-cdn.alpinelinux.org " + pkgHostsMark + "\n151.101.66.132 dl-cdn.alpinelinux.org " + pkgHostsMark + "\n"
		if b, _ := os.ReadFile(hosts); string(b) != want || !pinned(root) {
			t.Errorf("hosts while the packs install (pinned=%v):\n%s", pinned(root), b)
		}
		// When pack add returns, they are gone.
		pp.ops.close()
		if b, _ := os.ReadFile(hosts); string(b) != userHosts || pinned(root) {
			t.Errorf("hosts after pack add (pinned=%v):\n%q", pinned(root), b)
		}
	})

	// Android has no answer either: the tries a refresh gets, and an error
	// that says what failed.
	t.Run("no network", func(t *testing.T) {
		os.Remove(calls)
		androidAnswers(t, nil, errors.New("no such host"))
		pp, slept, logged := alpinePack(t, root)
		err := packRefresh(context.Background(), pp, nopRep{})
		var e *ui.UserError
		if !errors.As(err, &e) {
			t.Fatalf("want a user error, got %v\n%s", err, logged())
		}
		if e.Title != "Couldn't reach the package servers" || e.Kind != "network_dns" || !strings.Contains(e.What, "DNS: transient error") || strings.Contains(e.What+e.Err.Error(), "exit status") {
			t.Errorf("user error: %q %q %q (%v)", e.Title, e.Kind, e.What, e.Err)
		}
		if got := len(callsOf(calls)); got != 4 {
			t.Errorf("apk ran %d times, want 4", got)
		}
		if want := []time.Duration{5 * time.Second, 20 * time.Second, 30 * time.Second}; !reflect.DeepEqual(*slept, want) {
			t.Errorf("pauses %v, want %v", *slept, want)
		}
		if b, _ := os.ReadFile(hosts); string(b) != userHosts {
			t.Errorf("hosts:\n%q", b)
		}
	})
}

// The packs' own installs use the same code: the addresses are still there
// for them, and a lookup that fails only then gets them too.
func TestPackInstallAlpineDeadDNS(t *testing.T) {
	root := alpineRoot(t)
	hosts := filepath.Join(root, "etc/hosts")
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fakeTool(t, bin, "apk", `case "$1" in search) echo "$3"; exit 0;; info) exit 1;; esac
case "$*" in *--simulate*) echo "(1/1) Installing python3 (3.12)"; exit 0;; esac
echo "$*" >>`+calls+`
grep -q '^151.101.2.132 dl-cdn.alpinelinux.org ' `+hosts+` && exit 0
echo "ERROR: python3-3.12: DNS: transient error (try again later)"; exit 1`)
	androidAnswers(t, []string{"151.101.2.132"}, nil)
	real, err := conf.LoadPack("python")
	if err != nil {
		t.Fatal(err)
	}
	pack := *real
	pack.Post, pack.Files = "", nil // nothing of the pack's own runs on this machine
	pp, slept, logged := alpinePack(t, root)
	defer pp.ops.close()
	if err := packInstall(context.Background(), pp, &pack, nopRep{}); err != nil {
		t.Fatalf("the install didn't recover: %v\n%s", err, logged())
	}
	if got := callsOf(calls); len(got) != 2 || !strings.HasPrefix(got[0], "--no-progress add ") || got[0] != got[1] || len(*slept) != 0 {
		t.Errorf("apk add ran as %q (pauses %v), want twice, at once", got, *slept)
	}
	if !pp.failed("python") {
		t.Error("the pack isn't recorded")
	}
}

// pack add holds no distro lock, so an install or update of the same distro
// may be running with its package servers' addresses in /etc/hosts. Those
// aren't pack's to remove; a run with the lock removes what it finds.
func TestPackLeavesAnInstallsPkgHosts(t *testing.T) {
	root := alpineRoot(t)
	androidAnswers(t, nil, errors.New("not asked"))
	hosts := filepath.Join(root, "etc/hosts")
	theirs := userHosts + "151.101.2.132 dl-cdn.alpinelinux.org " + pkgHostsMark + "\n"
	os.WriteFile(hosts, []byte(theirs), 0o644)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fakeTool(t, bin, "apk", "exit 0")
	pp, _, logged := alpinePack(t, root)
	if err := packRefresh(context.Background(), pp, nopRep{}); err != nil {
		t.Fatalf("%v\n%s", err, logged())
	}
	pp.ops.close()
	if b, _ := os.ReadFile(hosts); string(b) != theirs {
		t.Errorf("pack add removed another run's addresses:\n%q", b)
	}
	lg, _ := bufLog()
	p, _ := apkOps(t, root, lg)
	if err := p.update(context.Background(), nopRep{}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(hosts); string(b) != userHosts {
		t.Errorf("an install kept an earlier run's addresses:\n%q", b)
	}
}

// The other families' pack refresh is what it was: the family's own command,
// once, no retry code and no mirror list.
func TestPackRefreshOtherFamilies(t *testing.T) {
	for _, id := range []string{"apt", "dnf", "pacman", "xbps"} {
		famReal, err := pkgmgr.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		calls := filepath.Join(t.TempDir(), "calls")
		fam := *famReal
		fam.Update = "echo run >>" + calls + "; echo 'E: Could not connect to the server'; exit 2"
		lg, _ := bufLog()
		pp := &packPlace{id: id, t: &proot.Target{Rootfs: t.TempDir(), Local: true}, fam: &fam, label: id}
		if pp.ops = packOps(pp, lg); pp.ops != nil {
			t.Errorf("%s: pack got the retry code", id)
		}
		err = packRefresh(context.Background(), pp, nopRep{})
		var e *ui.UserError
		if !errors.As(err, &e) || e.Title != refreshTitle || len(callsOf(calls)) != 1 {
			t.Errorf("%s: %v, %d runs", id, err, len(callsOf(calls)))
		}
	}
}

// Review of #9: only the main archive is checked. Debian's security archive
// without lists never stopped a refresh, and it doesn't now.
func TestSecurityArchiveAloneDoesntFailTheRefresh(t *testing.T) {
	const secFailed = `echo "W: Failed to fetch http://deb.debian.org/debian-security/dists/trixie-security/InRelease  Could not connect to deb.debian.org:80 (151.101.2.132), connection timed out"`
	d, famReal := loadDistroFam(t, "debian")

	// A fresh install: the main archive's lists arrive, the security
	// archive's don't, apt exits 0. One try, as on 2.1.5.
	t.Run("security missed", func(t *testing.T) {
		root, src := debianRoot(t)
		calls := filepath.Join(t.TempDir(), "calls")
		fam := *famReal
		fam.Repair = ""
		fam.Update = "echo run >>" + calls + "; " + listsFor(root, "http://deb.debian.org/debian", "trixie") + "; " + secFailed
		lg, logged := bufLog()
		p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, d, lg)
		var slept []time.Duration
		p.sleep = func(d time.Duration) { slept = append(slept, d) }
		if err := p.update(context.Background(), nopRep{}); err != nil {
			t.Fatalf("%v\n%s", err, logged)
		}
		if b, _ := os.ReadFile(src); string(b) != debianSources || len(callsOf(calls)) != 1 || len(slept) != 0 {
			t.Errorf("apt ran %d times, pauses %v, sources:\n%s", len(callsOf(calls)), slept, b)
		}
	})

	// deb.debian.org is down for both archives and only the main one has a
	// mirror that answers: the main archive moves, the refresh then passes
	// without the security lists.
	t.Run("main moved, security has no mirror", func(t *testing.T) {
		old := mirrorProbe
		mirrorProbe = func(_ context.Context, _, _, to string) string {
			if to == "http://ftp.debian.org/debian" {
				return ""
			}
			return "no Release file (connection refused)"
		}
		t.Cleanup(func() { mirrorProbe = old })
		root, src := debianRoot(t)
		fam := *famReal
		fam.Repair = ""
		fam.Update = `if grep -q "URIs: http://deb.debian.org/debian$" ` + src + `; then echo "E: Failed to fetch http://deb.debian.org/debian/dists/trixie/InRelease  Could not connect to deb.debian.org:80 (151.101.2.132), connection timed out"; exit 100; fi
` + listsFor(root, "http://ftp.debian.org/debian", "trixie") + "; " + secFailed
		lg, logged := bufLog()
		p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, d, lg)
		p.sleep = func(time.Duration) {}
		if err := p.update(context.Background(), nopRep{}); err != nil {
			t.Fatalf("the refresh on the mirror worked, the step didn't: %v\n%s", err, logged)
		}
		want := strings.Replace(debianSources, "URIs: http://deb.debian.org/debian\n", "URIs: http://ftp.debian.org/debian\n", 1)
		if b, _ := os.ReadFile(src); string(b) != want || !strings.Contains(logged.String(), "worked on attempt 3") {
			t.Errorf("sources:\n%s\n%s", b, logged)
		}
	})

	// The main archive is each apt distro's first mirror group.
	for id, first := range map[string]string{"debian": "http://deb.debian.org/debian", "ubuntu": "http://ports.ubuntu.com/ubuntu-ports/", "ubuntu24": "http://ports.ubuntu.com/ubuntu-ports/", "kali": "http://kali.download/kali/"} {
		dd, _ := loadDistroFam(t, id)
		if g := dd.MirrorFallbacks(); len(g) == 0 || g[0][0] != first {
			t.Errorf("%s: the first mirror group is %v, want the main archive (%s)", id, g, first)
		}
	}
}

// Review of #9: pack add ends while an update of the same distro runs (it
// may be working with the addresses pack wrote): they stay for it, and go
// with the next proot start once no run holds the lock.
func TestPackEndLeavesPkgHostsToARunningUpdate(t *testing.T) {
	t.Setenv("ANDRONIX_HOME", t.TempDir())
	d, err := conf.LoadDistro("alpine")
	if err != nil {
		t.Fatal(err)
	}
	root := Open(d).Rootfs
	os.MkdirAll(filepath.Join(root, "etc/apk"), 0o755)
	os.WriteFile(filepath.Join(root, "etc/apk/repositories"), []byte("https://dl-cdn.alpinelinux.org/alpine/v3.24/main\n"), 0o644)
	hosts := filepath.Join(root, "etc/hosts")
	os.WriteFile(hosts, []byte(userHosts), 0o644)
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fakeTool(t, bin, "apk", `grep -q '^151.101.2.132 dl-cdn.alpinelinux.org ' `+hosts+` && exit 0; `+apkDNSFail)
	androidAnswers(t, []string{"151.101.2.132"}, nil)
	pp, _, logged := alpinePack(t, root)
	if err := packRefresh(context.Background(), pp, nopRep{}); err != nil {
		t.Fatalf("%v\n%s", err, logged())
	}
	pinnedHosts := userHosts + "151.101.2.132 dl-cdn.alpinelinux.org " + pkgHostsMark + "\n"
	unlock, err := lockDistro(d, "update")
	if err != nil {
		t.Fatal(err)
	}
	pp.ops.close()
	if b, _ := os.ReadFile(hosts); string(b) != pinnedHosts || pinned(root) {
		t.Errorf("pack add's end took the addresses from a running update (pinned=%v):\n%q", pinned(root), b)
	}
	unlock()
	dropStalePkgHosts(root)
	if b, _ := os.ReadFile(hosts); string(b) != userHosts {
		t.Errorf("the addresses stayed with no run alive:\n%q", b)
	}
}

// Review of #9: an apk error nothing names is not tried again by pack add
// (the second `apk add` reports a package whose script failed as installed),
// and inside the distro pack runs apk as it always has.
func TestPackKeepsItsOldWays(t *testing.T) {
	root := alpineRoot(t)
	androidAnswers(t, nil, errors.New("not asked"))
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; echo "ERROR: python3-3.12.post-install: script exited with error 127"; exit 1`)
	pp, slept, logged := alpinePack(t, root)
	err := pp.ops.run(context.Background(), pp.fam.Install([]string{"python3"}), 1, nopRep{})
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindOther || len(callsOf(calls)) != 1 || len(*slept) != 0 {
		t.Errorf("%v: apk ran %d times (pauses %v), want once\n%s", err, len(callsOf(calls)), *slept, logged())
	}
	// An install does try once more, as before.
	os.Remove(calls)
	lg, _ := bufLog()
	p, _ := apkOps(t, root, lg)
	if err := p.run(context.Background(), p.fam.Install([]string{"python3"}), 1, nopRep{}); err == nil || len(callsOf(calls)) != 2 {
		t.Errorf("install: %v, apk ran %d times, want twice", err, len(callsOf(calls)))
	}

	// Inside the distro.
	old := guestMark
	guestMark = filepath.Join(root, "etc/hosts") // a file that is there
	t.Cleanup(func() { guestMark = old })
	if ops := packOps(pp, lg); ops != nil {
		t.Error("pack inside the distro got the retry code")
	}
}
