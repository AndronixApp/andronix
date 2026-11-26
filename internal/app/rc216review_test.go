package app

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	clog "github.com/charmbracelet/log"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// The whole-branch review of rc-2.1.6 (ab129f9 against v2.1.5).

// fakeTool puts a shell script named name first on PATH.
func fakeTool(t *testing.T, bin, name, body string) {
	t.Helper()
	os.MkdirAll(bin, 0o755)
	if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

func callsOf(path string) []string {
	b, _ := os.ReadFile(path)
	if len(b) == 0 {
		return nil
	}
	return strings.Split(strings.TrimRight(string(b), "\n"), "\n")
}

// apkTimedOut is apk's output for a refresh whose two sources can't be
// connected to, as apk-tools 3.0.8 prints it (exit status 2).
const apkTimedOut = `echo "WARNING: updating and opening https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/APKINDEX.tar.gz: Operation timed out"; echo "WARNING: updating and opening https://dl-cdn.alpinelinux.org/alpine/v3.24/community/aarch64/APKINDEX.tar.gz: Operation timed out"; echo "2 unavailable, 0 stale; 16 distinct packages available"; exit 2`

// The tests never ask a real package server for a connection: none answers
// unless a test says so.
func init() { apkServersAnswer = func(context.Context, string) bool { return false } }

// serversAnswer sets what the installer's own question to the package
// servers finds, for one test, and counts the questions.
func serversAnswer(t *testing.T, yes bool) *int {
	t.Helper()
	old := apkServersAnswer
	n := new(int)
	apkServersAnswer = func(context.Context, string) bool { *n++; return yes }
	t.Cleanup(func() { apkServersAnswer = old })
	return n
}

// Review 1: an Alpine refresh that can't connect took far too long to fail.
// apk waits its timeout (60 s) for every address of every source: two
// sources, four addresses at Alpine's CDN, 8 minutes a try, and a connect
// failure got four tries. Now the refresh's first try runs with a 15 s
// timeout; when the servers don't answer the installer either, there is one
// more try, with 5 s, and none once 200 s have passed: 3 minutes at
// Alpine's CDN, under 4 and a half whatever the number of addresses.
func TestApkRefreshConnectIsBounded(t *testing.T) {
	root := alpineRoot(t)
	androidAnswers(t, nil, errors.New("down"))
	asked := serversAnswer(t, false)
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; `+apkTimedOut)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	lg, logged := bufLog()
	p, slept := apkOps(t, root, lg)

	err := p.update(context.Background(), nopRep{})
	var f *pkgmgr.Failure
	if !errors.As(err, &f) || f.Kind != pkgmgr.KindConnect {
		t.Fatalf("want a connect failure, got %v\n%s", err, logged)
	}
	first, quick, own := "--timeout 15 --no-progress update", "--timeout 5 --no-progress update", "--no-progress update"
	if got, want := callsOf(calls), []string{first, quick}; !reflect.DeepEqual(got, want) {
		t.Errorf("apk ran as %q, want %q", got, want)
	}
	if want := []time.Duration{5 * time.Second}; !reflect.DeepEqual(*slept, want) {
		t.Errorf("pauses %v, want %v", *slept, want)
	}
	if *asked != 1 {
		t.Errorf("the servers were asked %d times, want once (after the first try)", *asked)
	}
	// The worst case: every address of every source of every try waits out
	// its timeout (measured with four: 120 s at 15, 40 s at 5), and the
	// question to the servers takes all the time it has. A second try
	// starts only when the first and the question ended inside the budget.
	const sources = 2
	for addresses := 1; addresses <= 16; addresses++ {
		worst := time.Duration(sources*addresses*apkRefreshTimeout)*time.Second + apkProbeTimeout
		if worst < apkRefreshBudget {
			worst += 5*time.Second + time.Duration(sources*addresses*apkRetryTimeout)*time.Second
		}
		if limit := 4*time.Minute + 30*time.Second; addresses <= 8 && worst > limit {
			t.Errorf("%d addresses: a refresh on a dead network takes up to %v, want at most %v", addresses, worst, limit)
		}
		if addresses == 4 && worst != 3*time.Minute {
			t.Errorf("four addresses (Alpine's CDN): %v, want 3 minutes", worst)
		}
	}
	if e := pkgErr(refreshTitle, err).(*ui.UserError); e.Title != "Couldn't reach the package servers" || e.Kind != "network_connect" {
		t.Errorf("user error: %q %q", e.Title, e.Kind)
	}

	// More addresses (IPv6 too, all dropped): the first try alone takes 4
	// minutes, and no second one starts. The clock here moves 250 s between
	// two looks at it.
	os.Remove(calls)
	*slept = nil
	clock := time.Unix(1_800_000_000, 0)
	p.now = func() time.Time { clock = clock.Add(250 * time.Second); return clock }
	if err := p.update(context.Background(), nopRep{}); err == nil {
		t.Fatal("want the failure")
	}
	if got, want := callsOf(calls), []string{first}; !reflect.DeepEqual(got, want) || len(*slept) != 0 {
		t.Errorf("past the time budget: apk ran as %q (pauses %v), want %q and no pause", got, *slept, want)
	}
	p.now = time.Now

	// The next command of the same run starts with the first try's timeout
	// again, and a retry that works ends it.
	os.Remove(calls)
	fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; [ "$2" = 5 ] && exit 0; `+apkTimedOut)
	if err := p.update(context.Background(), nopRep{}); err != nil {
		t.Fatalf("the retry worked, the refresh didn't: %v", err)
	}
	if got, want := callsOf(calls), []string{first, quick}; !reflect.DeepEqual(got, want) {
		t.Errorf("second refresh: apk ran as %q, want %q", got, want)
	}

	// Outside the refresh: one retry, as before, apk's own timeout (a big
	// download may take its time), and no question to the servers.
	os.Remove(calls)
	*asked = 0
	fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; `+apkTimedOut)
	if err := p.run(context.Background(), p.fam.Install([]string{"nano"}), 1, nopRep{}); err == nil {
		t.Fatal("want the failure")
	}
	if got, want := callsOf(calls), []string{"--no-progress add nano", "--no-progress add nano"}; !reflect.DeepEqual(got, want) || *asked != 0 {
		t.Errorf("install: apk ran as %q (servers asked %d times), want %q", got, *asked, want)
	}

	// Only a command that is apk's own gets the option; 0 is apk's own timeout.
	if got := withApkTimeout("apk --no-progress update", 5); got != "apk --timeout 5 --no-progress update" {
		t.Errorf("withApkTimeout = %q", got)
	}
	for _, c := range []string{"true", `echo "apk update"; exit 2`, "xbps-install -S"} {
		if got := withApkTimeout(c, 5); got != c {
			t.Errorf("withApkTimeout(%q) = %q", c, got)
		}
	}
	if got := withApkTimeout("apk --no-progress update", 0); got != "apk "+own {
		t.Errorf("withApkTimeout(0) = %q", got)
	}
	// A name-lookup failure is quick (musl gives up after 5 s) and keeps
	// its four tries, each with the first try's timeout: nothing failed to
	// connect, so nothing is asked either.
	os.Remove(calls)
	fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; `+apkDNSFail)
	if err := p.update(context.Background(), nopRep{}); err == nil {
		t.Fatal("want the failure")
	}
	if got, want := callsOf(calls), []string{first, first, first, first}; !reflect.DeepEqual(got, want) || *asked != 0 {
		t.Errorf("DNS failure: apk ran as %q (servers asked %d times), want %q", got, *asked, want)
	}
	// The family's own command is untouched: the callers without the retry
	// code (the display's Xvfb, sound) run apk as 2.1.5 did.
	if fam, _ := pkgmgr.Get("apk"); fam.Update != "apk "+own {
		t.Errorf("apk Update = %q", fam.Update)
	}
}

// Review of #8: apk's --timeout also counts a stall in the middle of a
// download and a slow first byte, so a short one fails slow links that 60 s
// let through. No phone may be worse off than on 2.1.5: when a try fails
// and the server is there (it answers the installer, or it answered apk),
// the short try isn't counted and the failure gets the tries 2.1.5 gave it,
// with apk's own timeout.
func TestApkSlowLinkKeepsApksOwnTimeout(t *testing.T) {
	root := alpineRoot(t)
	androidAnswers(t, nil, errors.New("down"))
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	first, quick, own := "--timeout 15 --no-progress update", "--timeout 5 --no-progress update", "--no-progress update"
	stalled := `echo "WARNING: updating and opening https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/APKINDEX.tar.gz: I/O error"; echo "1 unavailable, 0 stale; 16 distinct packages available"; exit 1`
	// Each case: what apk does while it has a --timeout, and how it must
	// have been run. Without one it works, as on 2.1.5.
	for _, c := range []struct {
		name    string
		answers bool
		short   string // apk's output with a --timeout
		want    []string
		asked   int
	}{
		// A slow first byte reads as "Operation timed out", like a dead
		// network; the server accepting a connection tells them apart.
		{"slow first byte, the server answers", true, apkTimedOut, []string{first, own}, 1},
		// A stall mid-download is apk's "I/O error": the server was there.
		{"stall in the download", false, stalled, []string{first, own}, 0},
		{"server error", false, `echo "WARNING: updating https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/APKINDEX.tar.gz: HTTP 503: Service Unavailable"; exit 1`, []string{first, own}, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			os.Remove(calls)
			asked := serversAnswer(t, c.answers)
			fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; [ "$1" = --timeout ] || exit 0; `+c.short)
			lg, logged := bufLog()
			p, _ := apkOps(t, root, lg)
			if err := p.update(context.Background(), nopRep{}); err != nil {
				t.Fatalf("a refresh that works with apk's own timeout failed: %v\n%s", err, logged)
			}
			if got := callsOf(calls); !reflect.DeepEqual(got, c.want) || *asked != c.asked {
				t.Errorf("apk ran as %q (servers asked %d times), want %q (%d)", got, *asked, c.want, c.asked)
			}
		})
	}

	// A server that answers the installer while apk never gets through at
	// any timeout: after the short try, the tries 2.1.5 made (four for a
	// connect failure, with its pauses), with apk's own timeout, and no cap
	// by time.
	os.Remove(calls)
	serversAnswer(t, true)
	fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; `+apkTimedOut)
	lg, _ := bufLog()
	p, slept := apkOps(t, root, lg)
	clock := time.Unix(1_800_000_000, 0)
	p.now = func() time.Time { clock = clock.Add(10 * time.Minute); return clock }
	if err := p.update(context.Background(), nopRep{}); err == nil {
		t.Fatal("want the failure")
	}
	if got, want := callsOf(calls), []string{first, own, own, own, own}; !reflect.DeepEqual(got, want) {
		t.Errorf("apk ran as %q, want %q", got, want)
	}
	if want := []time.Duration{5 * time.Second, 5 * time.Second, 20 * time.Second, 30 * time.Second}; !reflect.DeepEqual(*slept, want) {
		t.Errorf("pauses %v, want %v (the short try's, then 2.1.5's)", *slept, want)
	}
	// A download that stalls at any timeout: the short try, then the two
	// tries 2.1.5 gave a failure it had no name for.
	os.Remove(calls)
	fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; `+stalled)
	p, _ = apkOps(t, root, lg)
	if err := p.update(context.Background(), nopRep{}); err == nil {
		t.Fatal("want the failure")
	}
	if got, want := callsOf(calls), []string{first, own, own}; !reflect.DeepEqual(got, want) {
		t.Errorf("stall at any timeout: apk ran as %q, want %q", got, want)
	}
	fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; `+apkTimedOut)

	// The network goes away after a try with apk's own timeout: the short
	// timeout is back for the next one.
	os.Remove(calls)
	n := 0
	old := apkServersAnswer
	apkServersAnswer = func(context.Context, string) bool { n++; return n == 1 }
	defer func() { apkServersAnswer = old }()
	p, _ = apkOps(t, root, lg)
	if err := p.update(context.Background(), nopRep{}); err == nil {
		t.Fatal("want the failure")
	}
	if got, want := callsOf(calls), []string{first, own, quick}; !reflect.DeepEqual(got, want) {
		t.Errorf("network gone again: apk ran as %q, want %q", got, want)
	}
	// Gone after two tries with apk's own timeout: the failure has had its
	// two counted tries, so that is the end.
	os.Remove(calls)
	n = 0
	apkServersAnswer = func(context.Context, string) bool { n++; return n <= 2 }
	p, _ = apkOps(t, root, lg)
	if err := p.update(context.Background(), nopRep{}); err == nil {
		t.Fatal("want the failure")
	}
	if got, want := callsOf(calls), []string{first, own, own}; !reflect.DeepEqual(got, want) || n != 3 {
		t.Errorf("gone after two long tries: apk ran as %q (asked %d times), want %q (3)", got, n, want)
	}
}

// Cancelled while the servers are being asked: no pause and no further try.
func TestApkCancelWhileAskingTheServers(t *testing.T) {
	root := alpineRoot(t)
	androidAnswers(t, nil, errors.New("down"))
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	fakeTool(t, bin, "apk", `echo "$*" >>`+calls+`; `+apkTimedOut)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	old := apkServersAnswer
	apkServersAnswer = func(context.Context, string) bool { cancel(); return false }
	defer func() { apkServersAnswer = old }()
	lg, _ := bufLog()
	p, slept := apkOps(t, root, lg)
	if err := p.update(ctx, nopRep{}); err == nil {
		t.Fatal("want the failure")
	}
	if got := callsOf(calls); len(got) != 1 || len(*slept) != 0 {
		t.Errorf("after the cancel: apk ran %d times, pauses %v; want one run and no pause", len(got), *slept)
	}
}

// The installer's own question to the package servers: a TCP connection to
// one of the sources' hosts, never to anything else.
func TestApkServersAnswer(t *testing.T) {
	ask := apkServersAnswerReal
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	// A port nothing listens on: taken, then given back.
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	closed := gone.Addr().String()
	gone.Close()
	repos := func(lines ...string) string {
		root := t.TempDir()
		os.MkdirAll(filepath.Join(root, "etc/apk"), 0o755)
		os.WriteFile(filepath.Join(root, "etc/apk/repositories"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		return root
	}
	bg := context.Background()
	if !ask(bg, repos("http://"+ln.Addr().String()+"/alpine/v3.24/main")) {
		t.Error("a listening server reads as not answering")
	}
	if ask(bg, repos("http://"+closed+"/alpine/v3.24/main")) {
		t.Error("a closed port reads as answering")
	}
	// One of several is enough.
	if !ask(bg, repos("http://"+closed+"/alpine/v3.24/main", "@edge http://"+ln.Addr().String()+"/alpine/edge/main")) {
		t.Error("the second source answers, the question said no")
	}
	// A commented-out source isn't asked; local sources have nobody to ask
	// and keep apk's own timeout.
	if ask(bg, repos("http://"+closed+"/alpine/v3.24/main", "# http://"+ln.Addr().String()+"/alpine/edge/main")) {
		t.Error("a commented-out source was asked")
	}
	if !ask(bg, repos("/media/cdrom/apks")) || !ask(bg, t.TempDir()) {
		t.Error("no server to ask must read as answering")
	}
	// A cancelled run isn't held up.
	ctx, cancel := context.WithCancel(bg)
	cancel()
	if ask(ctx, repos("http://"+ln.Addr().String()+"/x")) {
		t.Error("answered after a cancel")
	}

	want := []string{"dl-cdn.alpinelinux.org:443", "mirror.example.org:8080", "192.168.1.5:80", "[2001:db8::1]:443"}
	got := apkRepoAddrs(repos("# comment https://commented.example/x", "https://dl-cdn.alpinelinux.org/alpine/v3.24/main",
		"https://dl-cdn.alpinelinux.org/alpine/v3.24/community", "@testing http://mirror.example.org:8080/alpine/edge/testing  # trailing https://no.example/",
		"/media/cdrom/apks", "http://192.168.1.5/alpine", "https://[2001:db8::1]/alpine", "ftp://ftp.example/alpine"))
	if !reflect.DeepEqual(got, want) {
		t.Errorf("apkRepoAddrs = %q, want %q", got, want)
	}
}

// pacmanTools are stand-ins for gpg, pacman-key and pacman, with a keyring
// directory of their own. gpg prints ROOT/keys; pacman-key --populate fails
// the way a phone without a working gpg-agent does while ROOT/no-agent
// exists, and otherwise makes a key trusted.
type pacmanTools struct{ root, g, calls, stamp string }

func newPacmanTools(t *testing.T) *pacmanTools {
	t.Helper()
	root := t.TempDir()
	pt := &pacmanTools{root: root, g: filepath.Join(root, "gnupg"), calls: filepath.Join(root, "calls")}
	pt.stamp = filepath.Join(pt.g, ".andronix-keyring-ok")
	bin := filepath.Join(root, "bin")
	fakeTool(t, bin, "gpg", "cat "+root+"/keys 2>/dev/null")
	fakeTool(t, bin, "gpgconf", "exit 0")
	fakeTool(t, bin, "pacman-key", `echo "pacman-key $*" >>`+pt.calls+`
if [ "$1" = --populate ]; then
  [ -e `+root+`/no-agent ] && { echo "gpg: agent_genkey failed: No agent running"; echo "==> ERROR: Master key could not be signed."; exit 1; }
  echo 'pub:f:4096:1:77193F152BDBE6A6:' >>`+root+`/keys
fi
exit 0`)
	fakeTool(t, bin, "pacman", `echo "pacman $*" >>`+pt.calls+`; [ -e `+root+`/pacman-fails ] && { echo "error: failed to synchronize all databases (unexpected error)"; exit 1; }; exit 0`)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The process list is made up too (no gpg program running).
	os.MkdirAll(filepath.Join(root, "proc", "101"), 0o755)
	os.WriteFile(filepath.Join(root, "proc", "101", "comm"), []byte("sh\n"), 0o644)
	return pt
}

// here rewrites a command's keyring directory and process list to pt's.
func (pt *pacmanTools) here(cmd string) string {
	return strings.ReplaceAll(strings.ReplaceAll(cmd, "/etc/pacman.d/gnupg", pt.g), "/proc/", filepath.Join(pt.root, "proc")+"/")
}

// keyring makes the keyring directory with these keys, as an install left it.
func (pt *pacmanTools) keyring(keys string, stamped bool) {
	os.RemoveAll(pt.g)
	os.MkdirAll(pt.g, 0o700)
	os.Remove(pt.calls)
	os.WriteFile(filepath.Join(pt.root, "keys"), []byte(keys), 0o644)
	os.WriteFile(filepath.Join(pt.g, "pubring.gpg"), []byte("keys"), 0o644)
	os.WriteFile(filepath.Join(pt.g, "trustdb.gpg"), []byte("trust"), 0o644)
	if stamped {
		os.WriteFile(pt.stamp, nil, 0o644)
	}
}

const (
	masterKey  = "pub:u:4096:1:AAAAAAAAAAAAAAAA:\n"
	trustedKey = "pub:f:4096:1:77193F152BDBE6A6:\n"
)

// Review 2: an Arch or Manjaro install made by 2.1.5 has a working keyring
// and no stamp, so its first refresh on 2.1.6 runs pacman-key --init and
// --populate again. On a phone where gpg-agent won't start (support #119)
// that fails, and the refresh must go on with the keys the install has, as
// 2.1.5 did: through pkgOps.update (install, update) and through the callers
// that run the family's Update directly (pack add, the built-in display's
// Xvfb, the app's sound).
func TestPacmanUpgradedKeyringPopulateFails(t *testing.T) {
	pt := newPacmanTools(t)
	famReal, _ := pkgmgr.Get("pacman")
	fam := *famReal
	fam.Update, fam.Repair = pt.here(fam.Update), ""
	const sync = "pacman --noconfirm --noprogressbar --color never -Syy"
	noAgent := filepath.Join(pt.root, "no-agent")
	os.WriteFile(noAgent, nil, 0o644)
	tg := &proot.Target{Rootfs: t.TempDir(), Local: true}

	// As pack.go, display.go and sound_standalone.go run it.
	pt.keyring(masterKey+trustedKey, false)
	var out []string
	if err := tg.Run(context.Background(), fam.Update, nil, func(l string) { out = append(out, l) }); err != nil {
		t.Fatalf("a direct Update failed on a 2.1.5 keyring whose --populate fails: %v\n%s", err, strings.Join(out, "\n"))
	}
	if got, want := callsOf(pt.calls), []string{"pacman-key --init", "pacman-key --populate", sync}; !reflect.DeepEqual(got, want) {
		t.Errorf("calls %q, want %q", got, want)
	}
	if _, err := os.Stat(pt.stamp); err == nil {
		t.Error("a failed --populate stamped the keyring finished")
	}
	if !strings.Contains(strings.Join(out, "\n"), "going on with the keys this install already trusts") {
		t.Errorf("nothing says the keys were kept:\n%s", strings.Join(out, "\n"))
	}

	// Through pkgOps: no failure, no retry, the keyring untouched.
	pt.keyring(masterKey+trustedKey, false)
	lg, logged := bufLog()
	p := newPkgOps(tg, &fam, nil, lg)
	p.sleep = func(time.Duration) {}
	if err := p.update(context.Background(), nopRep{}); err != nil {
		t.Fatalf("update: %v\n%s", err, logged)
	}
	if strings.Contains(logged.String(), "failed") || p.keyringAgent {
		t.Errorf("the refresh was retried (keyringAgent=%v):\n%s", p.keyringAgent, logged)
	}
	if _, err := os.Stat(filepath.Join(pt.g, "pubring.gpg")); err != nil {
		t.Error("the keyring was removed")
	}

	// The same phone later, with an agent that works: --populate finishes
	// and the keyring is stamped; the update after that leaves it alone.
	os.Remove(noAgent)
	pt.keyring(masterKey+trustedKey, false)
	if err := p.update(context.Background(), nopRep{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pt.stamp); err != nil {
		t.Error("a finished --populate left no stamp")
	}
	os.Remove(pt.calls)
	if err := p.update(context.Background(), nopRep{}); err != nil {
		t.Fatal(err)
	}
	if got, want := callsOf(pt.calls), []string{sync}; !reflect.DeepEqual(got, want) {
		t.Errorf("stamped keyring: calls %q, want %q", got, want)
	}

	// PR #3's repair still stops a refresh that has no keys to go on with:
	// an install stopped during --init/--populate (no trusted key) on a
	// phone where they fail again is a keyring failure, not a sync.
	os.WriteFile(noAgent, nil, 0o644)
	pt.keyring(masterKey+"pub:-:4096:1:77193F152BDBE6A6:\n", false)
	out = nil
	err := tg.Run(context.Background(), fam.Update, nil, func(l string) { out = append(out, l) })
	if err == nil {
		t.Fatal("a keyring with no trusted key and a failed --populate went on to the sync")
	}
	if got := callsOf(pt.calls); len(got) != 2 || strings.Contains(strings.Join(got, " "), "-Syy") {
		t.Errorf("calls %q, want the two pacman-key steps and no sync", got)
	}
	if f := pkgmgr.Diagnose(out, err); f.Kind != pkgmgr.KindKeyring {
		t.Errorf("diagnosed as %s, want keyring (its retry starts gpg-agent itself)", f.Kind)
	}
}

// Review 2, the retry: when a run that printed gpg-agent's failure ends in
// something else (Android stopping pacman, say), it is diagnosed as a
// keyring failure, and that recovery wiped the keyring before making it
// again. A keyring with trusted keys must survive it: on a phone where the
// keys can't be made again it is the only one that install will ever have.
func TestKeyringResetKeepsTrustedKeys(t *testing.T) {
	pt := newPacmanTools(t)
	reset := pt.here(keyringReset)
	if reset == keyringReset {
		t.Fatalf("keyringReset doesn't name the keyring directory: %q", keyringReset)
	}
	run := func() string {
		t.Helper()
		out, err := sys.Command("/bin/sh", "-c", reset).CombinedOutput()
		if err != nil {
			t.Fatalf("keyringReset: %v\n%s", err, out)
		}
		return string(out)
	}
	pt.keyring(masterKey+trustedKey, false)
	if out := run(); !strings.Contains(out, "kept") || !strings.Contains(out, "socketdir:") {
		t.Errorf("output %q", out)
	}
	if _, err := os.Stat(filepath.Join(pt.g, "pubring.gpg")); err != nil {
		t.Error("a keyring with trusted keys was wiped")
	}
	// Half-made (support #119, a first install): cleared as before.
	pt.keyring(masterKey, false)
	run()
	if _, err := os.Stat(filepath.Join(pt.g, "pubring.gpg")); err == nil {
		t.Error("a keyring with no trusted key was kept")
	}
	if st, err := os.Stat(pt.g); err != nil || st.Mode().Perm() != 0o700 {
		t.Errorf("the keyring directory after the reset: %v %v", st, err)
	}

	// The run this is for: --populate failed, the sync went on and failed on
	// something Diagnose has no earlier name for. It reads as a keyring
	// failure, whose recovery is keyringReset.
	famReal, _ := pkgmgr.Get("pacman")
	fam := *famReal
	fam.Update = pt.here(fam.Update)
	os.WriteFile(filepath.Join(pt.root, "no-agent"), nil, 0o644)
	os.WriteFile(filepath.Join(pt.root, "pacman-fails"), nil, 0o644)
	pt.keyring(masterKey+trustedKey, false)
	var out []string
	err := (&proot.Target{Rootfs: t.TempDir(), Local: true}).Run(context.Background(), fam.Update, nil, func(l string) { out = append(out, l) })
	if err == nil {
		t.Fatal("the sync failed, the Update didn't")
	}
	if f := pkgmgr.Diagnose(out, err); f.Kind != pkgmgr.KindKeyring {
		t.Fatalf("the run reads as %s, not keyring; this test needs another scenario:\n%s", f.Kind, strings.Join(out, "\n"))
	}
}

// Review 4: every proot start drops package-server addresses "a killed run
// left" in /etc/hosts, in whatever andronix process starts it. A login or a
// desktop start in another session took a live install's addresses away.
// While the distro's lock is held they stay.
func TestPkgHostsOfALiveRunStay(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANDRONIX_HOME", home)
	d, err := conf.LoadDistro("alpine")
	if err != nil {
		t.Fatal(err)
	}
	root := Open(d).Rootfs
	os.MkdirAll(filepath.Join(root, "etc"), 0o755)
	hosts := filepath.Join(root, "etc/hosts")
	pinnedHosts := userHosts + "151.101.2.132 dl-cdn.alpinelinux.org " + pkgHostsMark + "\n"
	os.WriteFile(hosts, []byte(pinnedHosts), 0o644)
	t.Cleanup(func() { pkgHostsPinned.Delete(root) })

	// No lock file yet (nothing was ever installed with a lock): stale.
	if distroLockHeld(root) {
		t.Fatal("held with no lock file")
	}
	// An install of this distro is running; this "process" is another one
	// (it didn't write the lines).
	unlock, err := lockDistro(d, "install")
	if err != nil {
		t.Fatal(err)
	}
	if !distroLockHeld(root) {
		t.Fatal("the install's lock isn't seen")
	}
	dropStalePkgHosts(root)
	startSession(root, os.Getpid())
	if b, _ := os.ReadFile(hosts); string(b) != pinnedHosts {
		t.Fatalf("a proot start in another session removed a live install's addresses:\n%s", b)
	}
	// Another distro's lock doesn't keep them, and asking doesn't take the
	// lock away from anyone.
	debian, _ := conf.LoadDistro("debian")
	if distroLockHeld(Open(debian).Rootfs) {
		t.Error("debian reads as locked")
	}
	if _, err := lockDistro(d, "update"); err == nil {
		t.Error("the install's lock was lost to the probe")
	}
	// The install ended (or was killed: the lock goes with the process).
	unlock()
	if distroLockHeld(root) {
		t.Fatal("still held after the unlock")
	}
	u2, err := lockDistro(d, "update")
	if err != nil {
		t.Fatalf("the probe left the lock taken: %v", err)
	}
	u2()
	dropStalePkgHosts(root)
	if b, _ := os.ReadFile(hosts); string(b) != userHosts {
		t.Errorf("a killed run's addresses stayed with no run alive:\n%q", b)
	}

	// The run that holds the lock still clears what a run before it left,
	// at its first package command (pkgOps.prepare).
	os.WriteFile(hosts, []byte(pinnedHosts), 0o644)
	unlock, _ = lockDistro(d, "install")
	defer unlock()
	lg, _ := bufLog()
	fam, _ := pkgmgr.Get(d.Family)
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, fam, d, lg)
	if err := p.do(context.Background(), "exit 0", 0, nopRep{}, true); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(hosts); string(b) != userHosts {
		t.Errorf("the new run kept an earlier run's addresses:\n%q", b)
	}

	// A rootfs that isn't under ~/.andronix/distros has no lock to ask.
	for _, r := range []string{"/", t.TempDir(), filepath.Join(home, "distros", "alpine"), filepath.Join(home, "elsewhere", "alpine", "rootfs")} {
		if distroLockHeld(r) {
			t.Errorf("%s reads as locked", r)
		}
	}
}

// Review 5: on apt's refresh step a mirror switch had no "the new mirror's
// lists arrived" check (the retried command is the refresh, and apt-get
// update exits 0 when a server only couldn't be reached). The install then
// went on with the sources on a mirror it has no lists from: "Unable to
// locate package" (#341). Now the check runs when that refresh exits 0, and
// a switch that brought nothing is undone and ends in the network failure.
func TestSwitchDuringRefreshNeedsTheNewMirrorsLists(t *testing.T) {
	primary, next := "http://ports.ubuntu.com/ubuntu-ports/", "http://ftp.tu-chemnitz.de/pub/linux/ubuntu-ports/"
	failed := `echo "W: Failed to fetch ` + next + `dists/noble/InRelease  Could not connect to ftp.tu-chemnitz.de:80"`
	manyLines := `i=0; while [ $i -lt 90 ]; do echo "Setting up ca-certificates ($i)"; i=$((i+1)); done`
	for _, c := range []struct {
		name   string
		onNext func(root string) string // the refresh once the sources use the next mirror
		undone bool
	}{
		{"lists arrive", func(root string) string { return listsFor(root, next, "noble") }, false},
		{"no lists", func(string) string { return "true" }, true},
		{"old lists, refresh failed", func(root string) string { return listsFor(root, next, "noble") + " && " + failed }, true},
		// apt's warning is followed by more output than the retry code keeps.
		{"old lists, refresh failed, long output", func(root string) string {
			return listsFor(root, next, "noble") + " && " + failed + " && " + manyLines
		}, true},
		{"another suite failed", func(root string) string {
			return listsFor(root, next, "noble") + ` && echo "W: Failed to fetch ` + next + `dists/noble-backports/InRelease  Connection timed out"`
		}, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root := ubuntuRoot(t, primary, time.Now())
			before := sourcesOf(root)
			src := filepath.Join(root, "etc/apt/sources.list.d/ubuntu.sources")
			famReal, _ := pkgmgr.Get("apt")
			fam := *famReal
			fam.Repair = ""
			// The distro's own server can't be reached; the next mirror
			// "answers" as the case says.
			fam.Update = `if grep -q "` + next + `" ` + src + `; then ` + c.onNext(root) + `; else echo "E: Failed to fetch ` + primary + `dists/noble/InRelease  Could not connect to ports.ubuntu.com:80 (185.125.190.39). - connect (111: Connection refused)"; exit 100; fi`
			lg, logged := bufLog()
			p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, nil, lg)
			p.sleep = func(time.Duration) {}
			p.mirrors, p.cur, p.home = [][]string{{primary, next}}, []int{0}, []int{0}
			err := p.update(context.Background(), nopRep{})
			if !c.undone {
				if err != nil {
					t.Fatalf("the refresh on the new mirror worked, the step didn't: %v\n%s", err, logged)
				}
				if strings.Count(sourcesOf(root), next) != 2 || p.cur[0] != 1 {
					t.Errorf("sources (cur=%v):\n%s", p.cur, sourcesOf(root))
				}
				return
			}
			var f *pkgmgr.Failure
			if !errors.As(err, &f) || f.Kind != pkgmgr.KindConnect {
				t.Fatalf("want the connect failure that led to the switch, got %v\n%s", err, logged)
			}
			if sourcesOf(root) != before || p.cur[0] != 0 {
				t.Errorf("the sources aren't back on the server whose lists are on disk (cur=%v):\n%s", p.cur, sourcesOf(root))
			}
			if !strings.Contains(logged.String(), "the sources are on "+primary+" again") {
				t.Errorf("log:\n%s", logged)
			}
		})
	}

	// No switch, no check: a first refresh that exits 0 with nothing new is
	// what it always was.
	root := ubuntuRoot(t, primary, time.Time{})
	famReal, _ := pkgmgr.Get("apt")
	fam := *famReal
	fam.Update, fam.Repair = "true", ""
	lg, _ := bufLog()
	p := newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, nil, lg)
	p.mirrors, p.cur, p.home = [][]string{{primary, next}}, []int{0}, []int{0}
	if err := p.update(context.Background(), nopRep{}); err != nil || p.cur[0] != 0 {
		t.Errorf("plain refresh: %v cur=%v", err, p.cur)
	}

	// Two switches in one refresh (the first mirror refused too): the undo
	// goes back to where the step began, not one mirror back.
	third := "http://mirrors.tuna.tsinghua.edu.cn/ubuntu-ports/"
	root = ubuntuRoot(t, primary, time.Now())
	before := sourcesOf(root)
	src := filepath.Join(root, "etc/apt/sources.list.d/ubuntu.sources")
	fam.Update = `if grep -q "` + third + `" ` + src + `; then true; else echo "E: Failed to fetch dists/noble/InRelease  Could not connect to the server"; exit 100; fi`
	lg, logged := bufLog()
	p = newPkgOps(&proot.Target{Rootfs: root, Local: true}, &fam, nil, lg)
	p.sleep = func(time.Duration) {}
	p.mirrors, p.cur, p.home = [][]string{{primary, next, third}}, []int{0}, []int{0}
	if err := p.update(context.Background(), nopRep{}); err == nil {
		t.Fatalf("a refresh that brought no lists from the third mirror passed:\n%s", logged)
	}
	if sourcesOf(root) != before || p.cur[0] != 0 {
		t.Errorf("after two switches the sources aren't back on the first server (cur=%v):\n%s\n%s", p.cur, sourcesOf(root), logged)
	}
}

// Review 6: when the registry refuses a layer and the step falls back to our
// mirror's tarball, the tarball is a stored download like any other and gets
// the same space check, before a byte of it is fetched.
func TestRegistryFallbackChecksSpace(t *testing.T) {
	d, _ := conf.LoadDistro("debian")
	lg := &Logger{Logger: clog.New(io.Discard)}
	body := []byte("the mirror's rootfs tarball")
	im, _ := fakeRegistry(t, 403, []byte("layer"))
	mirror, gets := fakeMirror(t, body, "")
	t.Setenv("ANDRONIX_MIRROR", mirror.URL)
	t.Setenv("ANDRONIX_SOURCE", "")
	p := Paths{Cache: t.TempDir()}
	layers := filepath.Join(p.Cache, "layers-debian")
	os.MkdirAll(layers, 0o755)
	os.WriteFile(filepath.Join(layers, "half.part"), []byte("half a layer"), 0o644)
	old := homeFreeMB
	t.Cleanup(func() { homeFreeMB = old })

	peak := downloadPeakMB(d, int64(len(body)))
	homeFreeMB = func() int64 { return peak - 1 }
	src := source{kind: "registry", image: im}
	called := false
	via, err := downloadRegistry(context.Background(), &src, d, "aarch64", p, lg, &lineRep{}, func() error { called = true; return nil })
	var ue *ui.UserError
	if !errors.As(err, &ue) || ue.Class != "not_enough_space" || !strings.Contains(ue.What, "MB free") {
		t.Fatalf("want not_enough_space, got %+v", err)
	}
	if called || gets.Load() != 0 {
		t.Errorf("the tarball download started without room for it (called=%v, %d downloads)", called, gets.Load())
	}
	if !via || src.kind != "tarball" {
		t.Errorf("via=%v src=%s: the step's source should be the tarball it couldn't fit", via, src.kind)
	}
	if _, err := os.Stat(layers); !os.IsNotExist(err) {
		t.Errorf("the layers are still on disk when the space is counted: %v", err)
	}

	// Room for it, and a phone whose free space can't be read: it goes on.
	for _, free := range []int64{peak, -1} {
		homeFreeMB = func() int64 { return free }
		src, called = source{kind: "registry", image: im}, false
		if via, err := downloadRegistry(context.Background(), &src, d, "aarch64", p, lg, &lineRep{}, func() error { called = true; return nil }); err != nil || !via || !called {
			t.Errorf("free=%d: via=%v called=%v err=%v", free, via, called, err)
		}
	}

	// The same check as the step's own: the stored kinds with a known size.
	homeFreeMB = func() int64 { return 1 }
	for _, c := range []struct {
		s    source
		want bool
	}{
		{source{kind: "tarball", size: 50 << 20}, true},
		{source{kind: "upstream", size: 50 << 20}, true},
		{source{kind: "tarball"}, false},                  // size unknown
		{source{kind: "modded", size: 900 << 20}, false},  // its own check
		{source{kind: "registry", size: 50 << 20}, false}, // no stored copy next to the rootfs
		{source{kind: "image", size: 900 << 20}, false},   // streamed
		{source{kind: "local", size: 50 << 20}, false},    // already here
	} {
		if got := downloadSpaceErr(d, c.s, lg) != nil; got != c.want {
			t.Errorf("%s size=%d: refused=%v, want %v", c.s.kind, c.s.size, got, c.want)
		}
	}
}

// Review 3, the report side: a password with an '@' of its own left its tail
// in the report ("https://<redacted>@ss@host").
func TestRedactURLCredentialsWithAt(t *testing.T) {
	r := &redactor{}
	for in, want := range map[string]string{
		"WARNING: updating https://bob:p@ss@repo.example.org/alpine/main: HTTP 403": "WARNING: updating https://<redacted>@repo.example.org/alpine/main: HTTP 403",
		"Err:1 http://tok_9f8e7d@apt.example.net/debian stable InRelease":           "Err:1 http://<redacted>@apt.example.net/debian stable InRelease",
		"https://dl-cdn.alpinelinux.org/alpine/v3.24/main":                          "https://dl-cdn.alpinelinux.org/alpine/v3.24/main",
		// A '?' in a password isn't the start of a query.
		"fetch https://bob:s3?cret@repo.example.org/x?e=1&h=2 failed": "fetch https://<redacted>@repo.example.org/x?<redacted> failed",
		// A '/', a quote or a bracket in a password (review of #8).
		"WARNING: updating https://bob:s3/cret@repo.example.org/alpine/main: HTTP 403": "WARNING: updating https://<redacted>@repo.example.org/alpine/main: HTTP 403",
		`curl: (22) "https://bob:s3'cr"et@repo.example.org/x" 403`:                     `curl: (22) "https://<redacted>@repo.example.org/x" 403`,
		"fetch https://bob:s3?cr/et@repo.example.org/x failed":                         "fetch https://<redacted>@repo.example.org/x failed",
		// An '@' in a path goes the same way: the host is lost, nothing leaks.
		"GET https://repo.example.org/pool/@scope/x.apk 404": "GET https://<redacted>@scope/x.apk 404",
	} {
		if got := r.do(in); got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
	}
}

// Review 8: rc1 is on the beta channel with other code; a plain build of
// this branch must not call itself that.
func TestVersionDefaultIsNotTheShippedRC(t *testing.T) {
	if Version == "2.1.6-rc1" {
		t.Error("Version is 2.1.6-rc1, which is on the beta channel already")
	}
}
