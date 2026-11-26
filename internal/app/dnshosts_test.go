package app

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	charmlog "github.com/charmbracelet/log"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Card #398: Alpine (musl, no NSS) asks only resolv.conf's public DNS
// servers. These are apk's own lines for both sources failing that way.
const apkDNSFail = `echo "WARNING: updating https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/APKINDEX.tar.gz: DNS: transient error (try again later)"; echo "WARNING: updating https://dl-cdn.alpinelinux.org/alpine/v3.24/community/aarch64/APKINDEX.tar.gz: DNS: transient error (try again later)"; echo "2 unavailable, 0 stale; 16 distinct packages available"; exit 2`

const userHosts = "127.0.0.1   localhost.localdomain localhost\n::1         localhost.localdomain localhost ip6-localhost ip6-loopback\n10.0.0.7 nas.home\n"

// alpineRoot is a rootfs with Alpine's sources and a hosts file.
func alpineRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc/apk"), 0o755)
	os.WriteFile(filepath.Join(root, "etc/apk/repositories"),
		[]byte("https://dl-cdn.alpinelinux.org/alpine/v3.24/main\nhttps://dl-cdn.alpinelinux.org/alpine/v3.24/community\n"), 0o644)
	os.WriteFile(filepath.Join(root, "etc/hosts"), []byte(userHosts), 0o644)
	return root
}

// androidAnswers makes the distro a no-NSS one on Android whose resolver
// answers with ips (or fails with err), and counts the lookups.
func androidAnswers(t *testing.T, ips []string, err error) *int {
	t.Helper()
	oldWanted, oldLookup := pkgHostsWanted, lookupForGuest
	n := new(int)
	pkgHostsWanted = func(*proot.Target) bool { return true }
	lookupForGuest = func(_ context.Context, host string) ([]net.IPAddr, error) {
		*n++
		if err != nil {
			return nil, err
		}
		var out []net.IPAddr
		for _, s := range ips {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return out, nil
	}
	t.Cleanup(func() {
		pkgHostsWanted, lookupForGuest = oldWanted, oldLookup
		pkgHostsPinned.Range(func(k, _ any) bool { pkgHostsPinned.Delete(k); return true })
		pmSaved = ""
	})
	return n
}

// pinned: this process holds the lines for root.
func pinned(root string) bool { _, ok := pkgHostsPinned.Load(root); return ok }

func bufLog() (*Logger, *bytes.Buffer) {
	var b bytes.Buffer
	return &Logger{Logger: charmlog.New(&b)}, &b
}

func apkOps(t *testing.T, root string, lg *Logger) (*pkgOps, *[]time.Duration) {
	t.Helper()
	d, err := conf.LoadDistro("alpine")
	if err != nil {
		t.Fatal(err)
	}
	fam, err := pkgmgr.Get(d.Family)
	if err != nil {
		t.Fatal(err)
	}
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, fam, d, lg)
	var slept []time.Duration
	p.sleep = func(d time.Duration) { slept = append(slept, d) }
	return p, &slept
}

func TestApkRepoHosts(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "etc/apk"), 0o755)
	os.WriteFile(filepath.Join(root, "etc/apk/repositories"), []byte(`# comment https://commented.example/x
https://dl-cdn.alpinelinux.org/alpine/v3.24/main
https://DL-CDN.alpinelinux.org/alpine/v3.24/community
@testing http://mirror.example.org:8080/alpine/edge/testing  # trailing https://no.example/
/media/cdrom/apks
http://192.168.1.5/alpine
https://bad_host!.example/x
ftp://ftp.example/alpine
`), 0o644)
	want := []string{"dl-cdn.alpinelinux.org", "mirror.example.org"}
	if got := apkRepoHosts(root); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := apkRepoHosts(t.TempDir()); got != nil {
		t.Errorf("no repositories file: %q", got)
	}
}

func TestPkgHostAddrs(t *testing.T) {
	ip := func(ss ...string) (out []net.IPAddr) {
		for _, s := range ss {
			out = append(out, net.IPAddr{IP: net.ParseIP(s)})
		}
		return
	}
	for _, c := range []struct {
		in   []net.IPAddr
		want []string
	}{
		{ip("2a04:4e42::644", "151.101.2.132", "151.101.66.132"), []string{"151.101.2.132", "151.101.66.132"}},
		{ip("2a04:4e42::644", "2a04:4e42:200::644"), []string{"2a04:4e42::644", "2a04:4e42:200::644"}},
		{ip("1.1.1.1", "1.1.1.2", "1.1.1.3", "1.1.1.4", "1.1.1.5"), []string{"1.1.1.1", "1.1.1.2", "1.1.1.3", "1.1.1.4"}},
		{ip("127.0.0.1", "0.0.0.0", "::1"), nil},
		{append(ip(), net.IPAddr{IP: net.ParseIP("fe80::1"), Zone: "wlan0"}), nil},
		{nil, nil},
	} {
		if got := pkgHostAddrs(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%v: got %q, want %q", c.in, got, c.want)
		}
	}
}

// The whole recovery: apk's DNS failure, Android's resolver answers, the
// retry works at once (no pause), and the run leaves /etc/hosts as it was.
func TestPkgHostsFromAndroidDNS(t *testing.T) {
	root := alpineRoot(t)
	lookups := androidAnswers(t, []string{"2a04:4e42::644", "151.101.2.132", "151.101.66.132"}, nil)
	lg, logged := bufLog()
	p, slept := apkOps(t, root, lg)
	hosts := filepath.Join(root, "etc/hosts")
	// "apk" works once dl-cdn is in /etc/hosts.
	cmd := "grep -q '^151.101.2.132 dl-cdn.alpinelinux.org ' " + hosts + " && exit 0; " + apkDNSFail
	if err := p.do(context.Background(), cmd, 0, nopRep{}, true); err != nil {
		t.Fatalf("didn't recover: %v\n%s", err, logged)
	}
	if len(*slept) != 0 {
		t.Errorf("paused %v before the retry; Android's addresses should be tried at once", *slept)
	}
	if *lookups != 1 {
		t.Errorf("%d lookups, want 1 (one host in two sources)", *lookups)
	}
	b, _ := os.ReadFile(hosts)
	want := userHosts + "151.101.2.132 dl-cdn.alpinelinux.org " + pkgHostsMark + "\n151.101.66.132 dl-cdn.alpinelinux.org " + pkgHostsMark + "\n"
	if string(b) != want {
		t.Errorf("hosts during the run:\n%s\nwant\n%s", b, want)
	}
	if st, _ := os.Stat(hosts); st.Mode().Perm() != 0o644 {
		t.Errorf("hosts mode %v", st.Mode().Perm())
	}
	if pmSaved != "network_dns_android_retried" || !pinned(root) {
		t.Errorf("pmSaved=%q pinned=%v", pmSaved, pinned(root))
	}
	// This process's own proot runs leave the lines; another distro's don't count.
	dropStalePkgHosts(root)
	startSession(root, os.Getpid())
	if b, _ := os.ReadFile(hosts); string(b) != want {
		t.Errorf("a proot start of the same run removed the lines:\n%s", b)
	}
	for _, s := range []string{"attempt 1 failed: network_dns: WARNING: updating dl-cdn.alpinelinux.org DNS: transient error", "using Android's addresses for dl-cdn.alpinelinux.org", "the retry with Android's addresses for the package servers worked"} {
		if !strings.Contains(logged.String(), s) {
			t.Errorf("log lacks %q:\n%s", s, logged)
		}
	}
	// A later command of the same run uses them without another lookup.
	if err := p.do(context.Background(), cmd, 0, nopRep{}, false); err != nil || *lookups != 1 {
		t.Errorf("second command: %v, %d lookups", err, *lookups)
	}
	p.close()
	p.close() // twice is fine
	if b, _ := os.ReadFile(hosts); string(b) != userHosts {
		t.Errorf("hosts after the run:\n%q\nwant\n%q", b, userHosts)
	}
	if pinned(root) {
		t.Error("still marked as pinned after close")
	}
}

// Android's addresses in place and the lookup still fails: no second
// write, no loop; the usual pauses, then the DNS failure.
func TestPkgHostsDontHelp(t *testing.T) {
	root := alpineRoot(t)
	lookups := androidAnswers(t, []string{"151.101.2.132"}, nil)
	lg, logged := bufLog()
	p, slept := apkOps(t, root, lg)
	err := p.do(context.Background(), apkDNSFail, 0, nopRep{}, true)
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindDNS {
		t.Fatalf("want a DNS failure, got %v", err)
	}
	if *lookups != 1 {
		t.Errorf("%d lookups, want 1", *lookups)
	}
	// Attempt 2 follows the write at once; then the usual pauses.
	if !reflect.DeepEqual(*slept, []time.Duration{20 * time.Second, 30 * time.Second}) {
		t.Errorf("pauses %v", *slept)
	}
	if !strings.Contains(logged.String(), "didn't help (network_dns)") || pmSaved != "" {
		t.Errorf("pmSaved=%q log:\n%s", pmSaved, logged)
	}
	if e := pkgErr(refreshTitle, err).(*ui.UserError); e.Title != "Couldn't reach the package servers" || e.Kind != "network_dns" || !strings.Contains(e.What, "DNS: transient error") {
		t.Errorf("user error: %q %q %q", e.Title, e.Kind, e.What)
	}
	p.close()
	if b, _ := os.ReadFile(filepath.Join(root, "etc/hosts")); string(b) != userHosts {
		t.Errorf("hosts after the run: %q", b)
	}
}

// No network at all (Android's resolver fails too): nothing is written,
// the retries run as for any network failure.
func TestPkgHostsAndroidDownToo(t *testing.T) {
	root := alpineRoot(t)
	androidAnswers(t, nil, &net.DNSError{Err: "no such host", Name: "dl-cdn.alpinelinux.org", IsNotFound: true})
	lg, logged := bufLog()
	p, slept := apkOps(t, root, lg)
	err := p.do(context.Background(), apkDNSFail, 0, nopRep{}, true)
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindDNS {
		t.Fatalf("want a DNS failure, got %v", err)
	}
	if len(*slept) != 3 || pinned(root) {
		t.Errorf("pauses %v pinned=%v", *slept, pinned(root))
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/hosts")); string(b) != userHosts {
		t.Errorf("hosts changed: %q", b)
	}
	if !strings.Contains(logged.String(), "Android's DNS has no answer for dl-cdn.alpinelinux.org either") {
		t.Errorf("log:\n%s", logged)
	}
}

// Distros with the forwarder (glibc), other hosts than Android, and
// ANDRONIX_DNS=go never get the lines: no lookup, no write.
func TestPkgHostsOnlyWithoutNSS(t *testing.T) {
	root := alpineRoot(t)
	lookups := androidAnswers(t, []string{"151.101.2.132"}, nil)
	pkgHostsWanted = func(*proot.Target) bool { return false }
	lg, _ := bufLog()
	p, slept := apkOps(t, root, lg)
	if err := p.do(context.Background(), apkDNSFail, 0, nopRep{}, true); err == nil {
		t.Fatal("want the failure")
	}
	if *lookups != 0 || len(*slept) != 3 {
		t.Errorf("%d lookups, pauses %v", *lookups, *slept)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "etc/hosts")); string(b) != userHosts {
		t.Errorf("hosts changed: %q", b)
	}
	// The real gate: Android, Android's resolver in use, a distro under
	// proot, and no NSS modules in it (glibc has them, musl doesn't).
	glibc := t.TempDir()
	os.MkdirAll(filepath.Join(glibc, "usr/lib/aarch64-linux-gnu"), 0o755)
	os.WriteFile(filepath.Join(glibc, "usr/lib/aarch64-linux-gnu/libnss_files.so.2"), nil, 0o644)
	if nssLibDir(glibc) == "" || nssLibDir(root) != "" {
		t.Errorf("nssLibDir: glibc %q, alpine %q", nssLibDir(glibc), nssLibDir(root))
	}
	for _, c := range []struct {
		android, systemDNS, local bool
		nss                       string
		want                      bool
	}{
		{true, true, false, nssLibDir(root), true},
		{true, true, false, nssLibDir(glibc), false}, // the forwarder serves it
		{false, true, false, "", false},              // not a phone
		{true, false, false, "", false},              // ANDRONIX_DNS=go, or a build without Android's resolver
		{true, true, true, "", false},                // no proot (tests, inside a distro)
	} {
		if got := pkgHostsGate(c.android, c.systemDNS, c.local, c.nss); got != c.want {
			t.Errorf("gate(%v, %v, %v, %q) = %v", c.android, c.systemDNS, c.local, c.nss, got)
		}
	}
}

// Outside the refresh apk gets one retry on a network failure, not three:
// each run of a big install waits out a timeout per package.
func TestApkNetworkRetriesOutsideRefresh(t *testing.T) {
	root := alpineRoot(t)
	androidAnswers(t, nil, errors.New("down"))
	for _, c := range []struct {
		update bool
		runs   int
	}{{true, 4}, {false, 2}} {
		lg, logged := bufLog()
		p, _ := apkOps(t, root, lg)
		if err := p.do(context.Background(), apkDNSFail, 0, nopRep{}, c.update); err == nil {
			t.Fatal("want the failure")
		}
		if n := strings.Count(logged.String(), " failed: network_dns"); n != c.runs {
			t.Errorf("update=%v: %d runs, want %d\n%s", c.update, n, c.runs, logged)
		}
	}
}

// apk goes on after a package fails: by the end of a big install its error
// line is far behind the last 60 lines. It still names the failure, and
// reaches the app's log.
func TestApkErrorLineBeforeTheTail(t *testing.T) {
	root := alpineRoot(t)
	androidAnswers(t, nil, errors.New("down"))
	old := ui.JSON
	defer func() { ui.JSON = old }()
	ui.JSON = true
	cmd := `echo "(1/300) Installing a (1-r0)"; echo "ERROR: a-1-r0: DNS: transient error (try again later)"; i=2; while [ $i -lt 120 ]; do echo "($i/300) Installing p$i (1-r0)"; i=$((i+1)); done; echo "1 error; 100 MiB in 300 packages"; exit 1`
	lg, logged := bufLog()
	p, _ := apkOps(t, root, lg)
	err := p.do(context.Background(), cmd, 0, nopRep{}, false)
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindDNS {
		t.Fatalf("want a DNS failure, got %v", err)
	}
	if !strings.Contains(logged.String(), "package manager: output: ERROR: a-1-r0: DNS: transient error (try again later)") {
		t.Errorf("the error line isn't in the log:\n%s", logged)
	}
	// Other package managers keep the 60-line window as it was.
	d, _ := conf.LoadDistro("debian")
	fam, _ := pkgmgr.Get(d.Family)
	q := newPkgOps(&proot.Target{Rootfs: t.TempDir(), Local: true}, fam, d, lg)
	q.sleep = func(time.Duration) {}
	err = q.do(context.Background(), cmd, 0, nopRep{}, false)
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindOther {
		t.Errorf("apt: want other, got %v", err)
	}
}

// What a killed run left goes with the next run's first package command,
// and a hosts file that is a link out of the distro is never rewritten.
func TestPkgHostsLeftovers(t *testing.T) {
	root := alpineRoot(t)
	hosts := filepath.Join(root, "etc/hosts")
	stale := userHosts + "203.0.113.9 dl-cdn.alpinelinux.org " + pkgHostsMark + "\n# mine: " + pkgHostsMark + " (kept: the mark isn't at the end)\n"
	os.WriteFile(hosts, []byte(stale), 0o644)
	androidAnswers(t, []string{"151.101.2.132"}, nil)
	lg, _ := bufLog()
	p, _ := apkOps(t, root, lg)
	if err := p.do(context.Background(), "exit 0", 0, nopRep{}, true); err != nil {
		t.Fatal(err)
	}
	want := userHosts + "# mine: " + pkgHostsMark + " (kept: the mark isn't at the end)\n"
	if b, _ := os.ReadFile(hosts); string(b) != want {
		t.Errorf("after the next run's first command:\n%q\nwant\n%q", b, want)
	}

	// A link: left alone both ways.
	outside := filepath.Join(t.TempDir(), "hosts")
	os.WriteFile(outside, []byte(stale), 0o644)
	os.Remove(hosts)
	if err := os.Symlink(outside, hosts); err != nil {
		t.Skip(err)
	}
	if err := unpinPkgHosts(root); err != nil {
		t.Fatal(err)
	}
	q, _ := apkOps(t, root, lg)
	if q.pinPkgHosts(context.Background(), nopRep{}) {
		t.Error("wrote through a link")
	}
	if b, _ := os.ReadFile(outside); string(b) != stale {
		t.Errorf("the link's target changed: %q", b)
	}
	if st, _ := os.Lstat(hosts); st.Mode()&os.ModeSymlink == 0 {
		t.Error("the link was replaced")
	}

	// A line of the user's with the mark inside it is neither ours nor in
	// the way, and a file with nothing of ours isn't rewritten.
	os.Remove(hosts)
	mine := userHosts + "# mine: " + pkgHostsMark + " (kept)\n"
	os.WriteFile(hosts, []byte(mine), 0o600)
	before, _ := os.Stat(hosts)
	if err := unpinPkgHosts(root); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(hosts); !os.SameFile(before, after) {
		t.Error("rewrote a hosts file with none of our lines")
	}
	if !q.pinPkgHosts(context.Background(), nopRep{}) {
		t.Error("a user's line with the mark in it blocked the write")
	}
	if st, _ := os.Stat(hosts); st.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want the file's own 0600", st.Mode().Perm())
	}
	// Another process's proot start (not the writer) removes them.
	pkgHostsPinned.Delete(root)
	startSession(root, os.Getpid())
	if b, _ := os.ReadFile(hosts); string(b) != mine {
		t.Errorf("after a session start:\n%q\nwant\n%q", b, mine)
	}
	if st, _ := os.Stat(hosts); st.Mode().Perm() != 0o600 {
		t.Errorf("mode after removal %v", st.Mode().Perm())
	}

	// No hosts file at all: one is made, and removed lines leave it empty.
	os.Remove(hosts)
	if !q.pinPkgHosts(context.Background(), nopRep{}) {
		t.Fatal("no hosts file: not written")
	}
	q.close()
	if b, err := os.ReadFile(hosts); err != nil || len(b) != 0 {
		t.Errorf("after close: %q %v", b, err)
	}
}

// The app's progress mode drops the package manager's lines; a failed
// attempt's last lines go to the log there, so a problem report shows
// apk's own error (card #398). The other modes log every line already.
func TestFailedRunOutputInJSONLog(t *testing.T) {
	root := alpineRoot(t)
	androidAnswers(t, nil, errors.New("down"))
	old := ui.JSON
	defer func() { ui.JSON = old }()
	long := strings.Repeat("x", 400)
	cmd := `i=0; while [ $i -lt 30 ]; do echo "line $i"; i=$((i+1)); done; echo; echo "` + long + `"; ` + apkDNSFail
	for _, json := range []bool{true, false} {
		ui.JSON = json
		lg, logged := bufLog()
		p, _ := apkOps(t, root, lg)
		p.do(context.Background(), cmd, 0, nopRep{}, true)
		s := logged.String()
		has := strings.Contains(s, "package manager: output: WARNING: updating https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/APKINDEX.tar.gz: DNS: transient error (try again later)") &&
			strings.Contains(s, "package manager: output: 2 unavailable, 0 stale; 16 distinct packages available")
		if has != json {
			t.Errorf("JSON=%v: output in the log = %v\n%s", json, has, s)
		}
		if !json {
			continue
		}
		if strings.Contains(s, "output: line 17") || !strings.Contains(s, "output: line 19") {
			t.Errorf("want the last %d lines of each attempt only:\n%s", pkgTailLines, s)
		}
		if strings.Contains(s, strings.Repeat("x", 301)) || !strings.Contains(s, strings.Repeat("x", 300)) {
			t.Error("a long line isn't cut to 300 characters")
		}
		if n := strings.Count(s, "output: 2 unavailable"); n != 4 {
			t.Errorf("%d attempts logged, want 4", n)
		}
	}
	// A cancelled run logs nothing more.
	ui.JSON = true
	lg, logged := bufLog()
	p, _ := apkOps(t, root, lg)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p.do(ctx, cmd, 0, nopRep{}, true)
	if strings.Contains(logged.String(), "package manager: output:") {
		t.Errorf("cancelled:\n%s", logged)
	}
}

func TestOutputTail(t *testing.T) {
	got := outputTail([]string{"a", "", "  ", "b \t", "c"}, 2)
	if !reflect.DeepEqual(got, []string{"b", "c"}) {
		t.Errorf("got %q", got)
	}
	if got := outputTail(nil, 5); len(got) != 0 {
		t.Errorf("got %q", got)
	}
}
