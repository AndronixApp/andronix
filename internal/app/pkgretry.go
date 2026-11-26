package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// pkgOps runs package-manager commands for one distro and recovers from
// what phones hit most (diagnose names it): a network hiccup is retried
// after a pause, then on the next mirror (DISTRO_MIRROR_FALLBACK); an
// interrupted dpkg is configured, stale or corrupt lists are refetched, a
// wrong clock stops failing apt's date checks, broken dependencies get
// apt-get -f install. A refresh where only some sources failed goes on
// with the lists it has (apt).
type pkgOps struct {
	t       *proot.Target
	fam     *pkgmgr.Family
	lg      *Logger
	mirrors [][]string // per group: the URL the sources use now is mirrors[g][cur[g]]
	cur     []int
	sleep   func(time.Duration)
	ready   bool // the sources were read (they exist once the rootfs is unpacked)
	// liveHolder: the package lock belongs to another running andronix
	// (its pid), support #313.
	liveHolder int
	// seccompRetry: the next attempt is a retry with proot on ptrace only
	// (PROOT_NO_SECCOMP=1), for this kind (#312 fake_root, #471 proot_syscall).
	seccompRetry pkgmgr.Kind
	// l2sRetry: the next attempt dropped proot's --link2symlink (experiment
	// #319, opt-in via ANDRONIX_L2S_RETRY=1), for this kind; the outcome is
	// logged and kept as <kind>_l2s_retried / <kind>_l2s_not_fixed.
	l2sRetry pkgmgr.Kind
	l2sTried bool // the --link2symlink-off retry has fired this run
	// prootSegv: the last attempt's captured output had a proot "signal 11".
	prootSegv bool
	// keyringAgent: start gpg-agent ourselves inside each retried run
	// (a keyring failure, support #119).
	keyringAgent bool
	// noUpdateHooks: refresh without apt's APT::Update hooks (one failed,
	// and no hook file we know of matched it).
	noUpdateHooks bool
	// #341: a mirror fallback switched the sources this run (switched), and
	// whether the index re-sync for the new mirror failed (indexStale). With
	// a stale index the caller stops with the network failure instead of
	// retrying the install against the old mirror's stale or empty lists and
	// reporting packages "not found" / "not in repository pool".
	switched   bool
	indexStale bool
	// keyringRefresh is the pacman distros' DISTRO_PRE_UPGRADE (pull the
	// current *-keyring packages and re-populate), run again when a package
	// still fails its PGP signature check (#354/#283).
	keyringRefresh string
	keyringRetried bool
	// keyringRebuild makes the local pacman keyring whole again (pacman-key
	// --init and --populate, no keyserver), run before keyringRefresh.
	keyringRebuild string
	// home is each group's own server (the distro's, not a fallback): the
	// first URL, or the one DISTRO_MIRROR_PRIMARY names. wentHome: the
	// sources went back to it this run (#399), once. left: the servers this
	// run moved away from; nextMirror doesn't pick them again. moves: the
	// last switch, for resync's check and its undo. probeFailed: the mirrors
	// that failed the check during the current command.
	home        []int
	wentHome    bool
	left        map[string]bool
	moves       []mirrorMove
	probeFailed map[string]bool
	// androidDNSRetry: the package servers' addresses were just written to
	// the distro's /etc/hosts from Android's resolver (dnshosts.go); the
	// next attempt is the retry with them.
	androidDNSRetry bool
	// apkSecs: the --timeout the next try of this apk refresh runs with, 0
	// for apk's own (see apkRefreshTimeout). began: when the current
	// command started, by now (time.Now; the tests' clock).
	apkSecs int
	began   time.Time
	now     func() time.Time
	// moveOnly: the groups the next mirror switch may move, nil for all of
	// them. Set while the failure is a refresh that left out the main
	// archive's server (aptMainMissed): the others answered and stay where
	// they are.
	moveOnly map[int]bool
	// shared: this run doesn't hold the distro's lock (pack add), so an
	// install or update of the same distro may be running: its package-server
	// addresses in /etc/hosts aren't this run's to remove (prepare, close).
	// Such a run also fails at once on an error nothing here names, as pack
	// always has (recover).
	shared bool
	// apt found no version of a package (package_not_found). relisted: the
	// package lists were fetched again for it, once a run (relist). noLists:
	// that ended without lists from the main archive, on any mirror; every
	// later "not found" of this run is that failure. relistRetry: the next
	// attempt is the retry with the lists that did arrive.
	relisted    bool
	noLists     *pkgmgr.Failure
	relistRetry bool
}

// mirrorMove: group g's sources moved away from its was-th server.
type mirrorMove struct{ g, was int }

// withoutUpdateHooks points the first apt-get in cmd (the update, not the
// ca-certificates install after it) at noUpdateHooksConf.
func withoutUpdateHooks(cmd string) string {
	return strings.Replace(cmd, "apt-get ", "apt-get -c "+noUpdateHooksConf+" ", 1)
}

// noUpdateHooksText is noUpdateHooksConf's content.
const noUpdateHooksText = "// Andronix: one refresh without apt's update hooks (one failed under proot).\n" +
	"#clear APT::Update::Pre-Invoke;\n#clear APT::Update::Post-Invoke;\n#clear APT::Update::Post-Invoke-Success;\n"

// noUpdateHooksConf clears apt's update hooks for one run (apt-get -c
// reads it after apt.conf.d, so the #clear wins).
const noUpdateHooksConf = "/etc/apt/andronix-no-update-hooks.conf"

// pkgAttempts caps the runs of one command (first try included).
const pkgAttempts = 5

// apk's --timeout for the refresh: seconds without progress, for each
// address of each source. apk's own is 60 s; a refresh has two sources and
// Alpine's CDN has four IPv4 addresses (and four IPv6), so one try on a
// network that resolves names and drops the rest took 8 minutes, and the
// four tries a connect failure got (in 2.1.5 too) over half an hour.
//
// A short timeout alone would fail a slow link that 60 s let through (the
// option also counts a stall in the middle of a download), so it is only
// kept while the network looks dead. A refresh's first try runs with
// apkRefreshTimeout. When a try fails after the server answered (anything
// but a failed lookup or connection), the next ones have apk's own timeout.
// When it couldn't connect, the installer asks the package servers for a
// connection itself (apkServersAnswer): one that answers means a slow link,
// and the next try gets apk's own timeout; none means the network is down,
// and the next try gets apkRetryTimeout. A try cut short by our timeout on
// a link that turned out to be there isn't counted: the failure then gets
// the tries it got in 2.1.5, all with apk's own timeout.
//
// Measured with apk-tools 3.0.8 (the only apk this installer has shipped,
// Alpine 3.24) against addresses that drop everything: 2 sources x 4
// addresses took 120 s at 15 and 40 s at 5. The option goes before the
// command.
const (
	apkRefreshTimeout = 15
	apkRetryTimeout   = 5
)

// With the servers not answering, a refresh gets apkConnectTries tries, and
// no second one when apkRefreshBudget has passed since it began by the time
// the first has failed and the servers were asked. Four addresses: 120 s,
// the question (up to apkProbeTimeout), a 5 s pause, 40 s: 3 minutes. Eight
// that all drop: one try and the question, 4 minutes 15. The worst is six
// (180 s, 15, 5, 60): 4 minutes 20.
//
// apkProbeTimeout is longer than the 10 s the installer's lookup gives
// Android's resolver, so a resolver that hangs still leaves time for the
// public servers and the connection.
const (
	apkConnectTries  = 2
	apkRefreshBudget = 200 * time.Second
	apkProbeTimeout  = 15 * time.Second
)

// withApkTimeout gives an apk command a --timeout of secs (0: apk's own).
func withApkTimeout(cmd string, secs int) string {
	if secs <= 0 || !strings.HasPrefix(cmd, "apk ") {
		return cmd
	}
	return fmt.Sprintf("apk --timeout %d %s", secs, strings.TrimPrefix(cmd, "apk "))
}

// apkRepoAddrs lists host:port of the distro's http and https apk sources.
func apkRepoAddrs(rootfs string) []string {
	b, _ := os.ReadFile(filepath.Join(rootfs, "etc/apk/repositories"))
	var out []string
	seen := map[string]bool{}
	for _, l := range strings.Split(string(b), "\n") {
		for _, w := range strings.Fields(l) {
			if strings.HasPrefix(w, "#") {
				break
			}
			u, err := url.Parse(w)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
				continue
			}
			port := u.Port()
			if port == "" {
				port = map[string]string{"http": "80", "https": "443"}[u.Scheme]
			}
			if a := net.JoinHostPort(u.Hostname(), port); !seen[a] {
				seen[a] = true
				out = append(out, a)
			}
		}
	}
	return out
}

// apkServersAnswer reports whether one of the distro's package servers
// accepts a connection from this phone right now, asked with the
// installer's own dialer (Android's resolver, IPv4 first; proot shares the
// phone's network with the distro). With no server to ask (local sources)
// it says yes: apk then keeps the timeout it always had. apkServersAnswer
// is a variable for the tests.
var apkServersAnswer = apkServersAnswerReal

func apkServersAnswerReal(ctx context.Context, rootfs string) bool {
	addrs := apkRepoAddrs(rootfs)
	if len(addrs) == 0 {
		return true
	}
	ctx, cancel := context.WithTimeout(ctx, apkProbeTimeout)
	defer cancel()
	ok := make(chan bool, len(addrs))
	for _, a := range addrs {
		go func() {
			c, err := sys.DialContext(ctx, "tcp", a)
			if err == nil {
				c.Close()
			}
			ok <- err == nil
		}()
	}
	for range addrs {
		if <-ok {
			return true
		}
	}
	return false
}

func newPkgOps(t *proot.Target, fam *pkgmgr.Family, d *conf.Distro, lg *Logger) *pkgOps {
	p := &pkgOps{t: t, fam: fam, lg: lg, sleep: time.Sleep, now: time.Now}
	if d != nil {
		p.mirrors = d.MirrorFallbacks()
		p.cur = make([]int, len(p.mirrors))
		p.home = make([]int, len(p.mirrors))
		for g, list := range p.mirrors {
			for i, u := range list {
				for _, h := range d.MirrorPrimaries() {
					if u == h {
						p.home[g] = i
					}
				}
			}
		}
		if fam != nil && fam.ID == "pacman" {
			p.keyringRefresh = d.PreUpgrade
		}
	}
	if fam != nil && fam.ID == "pacman" {
		p.keyringRebuild = pkgmgr.PacmanKeyringRebuild
	}
	return p
}

// update refreshes the package lists, then repairs what an interrupted
// package run left behind (the repair needs the fresh lists).
func (p *pkgOps) update(ctx context.Context, r ui.Reporter) error {
	if err := p.do(ctx, p.fam.Update, 0, r, true); err != nil {
		return err
	}
	if p.fam.Repair == "" {
		return nil
	}
	return p.do(ctx, p.fam.Repair, 0, r, false)
}

// run runs cmd (n packages, for the progress bar).
func (p *pkgOps) run(ctx context.Context, cmd string, n int, r ui.Reporter) error {
	return p.do(ctx, cmd, n, r, false)
}

// prepare reads which mirror the sources use, once the rootfs is there.
func (p *pkgOps) prepare() {
	if p.ready {
		return
	}
	p.ready = true
	// A run that was killed may have left its package-server addresses in
	// /etc/hosts (dnshosts.go); they are only good for the run that wrote them.
	// A run without the distro's lock leaves them: they may be a live
	// install's, and a killed run's go at this run's first proot start
	// (dropStalePkgHosts, which asks the lock).
	if !p.shared {
		if err := unpinPkgHosts(p.t.Rootfs); err != nil {
			p.lg.Printf("package manager: removing an earlier run's package-server addresses from /etc/hosts: %v", err)
		}
	}
	// An earlier run may have switched mirrors already: go on from there.
	for g, list := range p.mirrors {
		for i, u := range list {
			if pkgmgr.SourcesUse(p.t.Rootfs, u) {
				p.cur[g] = i
				break
			}
		}
	}
	// Tests: point the sources at a dead server first, so the fallback runs.
	if os.Getenv("ANDRONIX_TEST_BREAK_MIRROR") == "1" && len(p.mirrors) > 0 {
		dead := "http://127.0.0.1:9/andronix-broken/"
		if err := pkgmgr.RewriteBase(p.t.Rootfs, p.mirrors[0][p.cur[0]], dead); err == nil {
			// The distro's own server keeps its place after the dead one.
			if p.home[0] = p.home[0] - p.cur[0] + 1; p.home[0] < 1 {
				p.home[0] = 1
			}
			p.mirrors[0] = append([]string{dead}, p.mirrors[0][p.cur[0]:]...)
			p.cur[0] = 0
			p.lg.Printf("package manager: test: sources point at %s", dead)
		}
	}
}

func (p *pkgOps) do(ctx context.Context, cmd string, n int, r ui.Reporter, isUpdate bool) error {
	p.prepare()
	p.probeFailed = nil
	defer func() { p.moveOnly = nil }()
	p.apkSecs, p.began = apkRefreshTimeout, p.now()
	// apt's refresh step: where the sources stood when it began, to check a
	// mirror switch made during it once a retry exits 0 (switchedForNothing).
	aptRefresh := isUpdate && p.fam.ID == "apt"
	var from []int
	if aptRefresh {
		from = append(from, p.cur...)
	}
	var f *pkgmgr.Failure
	seen := map[pkgmgr.Kind]int{}
	for attempt := 1; attempt <= pkgAttempts; attempt++ {
		var lines, early, unfetched []string
		p.moveOnly = nil
		run := cmd
		if p.fam.ID == "apk" && isUpdate {
			run = withApkTimeout(run, p.apkSecs)
		}
		if p.keyringAgent {
			run = keyringAgentWrap(run)
		}
		if p.noUpdateHooks && isUpdate {
			run = withoutUpdateHooks(run)
		}
		err := pkgRunWatch(ctx, p.t, p.fam, run, n, r, func(l string) {
			// Kept apart from the 60-line window: the ca-certificates
			// install after the update can push them out of it.
			if aptRefresh && len(unfetched) < 200 && strings.Contains(l, "Failed to fetch") {
				unfetched = append(unfetched, l)
			}
			if lines = append(lines, l); len(lines) > 60 {
				// apk goes on after a package fails, so its error line can
				// be long gone from the window by the end: keep those.
				if p.fam.ID == "apk" && pkgmgr.ApkErrorLine(lines[0]) {
					if early = append(early, lines[0]); len(early) > pkgEarlyLines {
						early = early[1:]
					}
				}
				lines = lines[1:]
			}
		})
		if p.keyringAgent {
			p.t.Run(ctx, "tail -n 20 "+keyringAgentLog+" 2>/dev/null", nil, func(l string) { p.lg.Printf("keyring agent: %s", l) })
		}
		p.prootSegv = prootSignal11(lines)
		if err == nil && aptRefresh && p.switchedForNothing(from, unfetched) {
			// The last failure stands (f; p.switched keeps the "going on
			// with the lists apt has" below out of it). This try proved
			// nothing about the retries it may have been.
			p.seccompRetry, p.l2sRetry, p.androidDNSRetry, p.relistRetry = "", "", false, false
			break
		}
		// apt-get update exits 0 when a server only couldn't be reached. With
		// one server for everything the ca-certificates install after it then
		// fails and the refresh is retried; Debian's security server is another
		// one, so that install worked, the refresh passed without a single
		// list from the main archive, and the install stopped two steps later
		// at "Package adduser has no installation candidate" (2.1.6-rc2 gates;
		// 2.1.5 too). The main archive's server that this refresh failed to
		// fetch and that apt has no lists from at all is a failed refresh:
		// tried again and moved to the next mirror like any other, that group
		// only. Another group without lists (the security archive) is no
		// reason to stop: the install works from the main archive, as before.
		missed := ""
		if err == nil && aptRefresh {
			if missed = p.aptMainMissed(unfetched); missed != "" {
				err = errMainMissed
				p.moveOnly = map[int]bool{0: true}
			}
		}
		if err == nil {
			if attempt > 1 {
				p.lg.Printf("package manager: worked on attempt %d", attempt)
			}
			if p.androidDNSRetry {
				p.androidDNSRetry = false
				p.lg.Printf("package manager: network_dns: the retry with Android's addresses for the package servers worked")
				pmSaved = "network_dns_android_retried"
			}
			if p.noUpdateHooks && isUpdate {
				p.lg.Printf("package manager: the refresh worked without apt's update hooks")
				pmSaved = "apt_hook_retried"
			}
			if p.seccompRetry != "" {
				p.lg.Printf("package manager: %s: the retry with PROOT_NO_SECCOMP=1 worked", p.seccompRetry)
				pmSaved = string(p.seccompRetry) + "_retried"
				p.seccompRetry = ""
			}
			if p.l2sRetry != "" {
				p.lg.Printf("package manager: %s: the retry with --link2symlink off worked", p.l2sRetry)
				pmSaved = string(p.l2sRetry) + "_l2s_retried"
				p.l2sRetry = ""
			}
			if p.relistRetry {
				p.relistRetry = false
				p.lg.Printf("package manager: package_not_found: the retry with fresh package lists worked")
				pmSaved = "package_not_found_retried"
			}
			return nil
		}
		if missed != "" {
			// Named from apt's own "Failed to fetch" line for that server;
			// apt only lets a failure it takes for the network pass with a
			// warning, so that is what an unnamed one is.
			if f = pkgmgr.Diagnose([]string{missed}, err); f.Kind == pkgmgr.KindOther {
				f.Kind, f.Detail, f.Line = pkgmgr.KindConnect, pkgmgr.CleanDetail(missed), missed
			}
			p.lg.Printf("package manager: the refresh passed without any package lists from the distro's main archive (%s)", f.Detail)
		} else {
			f = pkgmgr.Diagnose(append(append([]string{}, early...), lines...), err)
		}
		if f.Kind == pkgmgr.KindNotFound && p.fam.ID != "apt" {
			f.Kind = pkgmgr.KindOther // apt's words; from anything else, as before
		}
		if f.Kind == pkgmgr.KindOther && p.fam.ID == "apt" {
			// dpkg doesn't always say "very bad inconsistent state": with
			// a dependent configured first it says the half-installed
			// package "is not installed" (support #201). Ask dpkg.
			if half := p.halfInstalled(ctx); half != "" {
				f.Kind, f.Detail = pkgmgr.KindReinstreq, "half-installed: "+half
			}
		}
		seen[f.Kind]++
		if p.androidDNSRetry {
			p.androidDNSRetry = false
			p.lg.Printf("package manager: network_dns: the retry with Android's addresses for the package servers didn't help (%s)", f.Kind)
		}
		p.lg.Printf("package manager: attempt %d failed: %s: %s", attempt, f.Kind, f.Detail)
		// The app's progress mode (--progress json) drops the package
		// manager's lines, so its logs, and the problem reports made from
		// them, had only "exit status 2" (card #398). The other modes log
		// every line already.
		if ui.JSON && f.Kind != pkgmgr.KindCancelled {
			for _, l := range append(outputTail(early, pkgEarlyLines), outputTail(lines, pkgTailLines)...) {
				p.lg.Printf("package manager: output: %s", l)
			}
		}
		if k := p.seccompRetry; k != "" {
			p.seccompRetry = ""
			p.lg.Printf("package manager: %s: the retry with PROOT_NO_SECCOMP=1 didn't help (%s)", k, f.Kind)
			if f.Kind == k {
				f.Kind = k + "_not_fixed"
			}
		}
		if k := p.l2sRetry; k != "" {
			p.l2sRetry = ""
			p.lg.Printf("package manager: %s: the retry with --link2symlink off didn't help (%s)", k, f.Kind)
			if f.Kind == k {
				f.Kind = k + "_l2s_not_fixed"
			}
		}
		if p.relistRetry {
			p.relistRetry = false
			p.lg.Printf("package manager: package_not_found: the retry with fresh package lists didn't help (%s)", f.Kind)
		}
		shortTry := p.fam.ID == "apk" && isUpdate && p.apkSecs != 0
		if attempt == pkgAttempts || !p.recover(ctx, f, seen[f.Kind], isUpdate, r) {
			break
		}
		// A try our own timeout cut short, on a link that is there after
		// all: not one of the tries this failure gets (see apkRefreshTimeout).
		if shortTry && p.apkSecs == 0 {
			seen[f.Kind]--
		}
		r.Line(fmt.Sprintf("Trying again (%s)...", strings.ReplaceAll(string(f.Kind), "_", " ")))
	}
	// The refresh's own ca-certificates install found no such package, and
	// the main archive has no lists: it is the lists that are missing, on
	// every mirror tried, not the package.
	if isUpdate && !p.shared && ctx.Err() == nil && f.Kind == pkgmgr.KindNotFound && p.aptNoLists() != "" {
		p.listsFailure(f, nil, nil)
	}
	// A refresh where only some sources failed goes on with the lists apt
	// has — but not once a mirror fallback has fired (!p.switched): the lists
	// on disk are then the abandoned mirror's, and carrying on with them is
	// the #341 bug (a later install reports packages "not found"). After a
	// switch, surface the refresh failure so the user sees the network error.
	// Nor when apt has no lists at all from the main archive's server (only
	// Debian's security lists are there): the next step would find no
	// package to install.
	if isUpdate && p.fam.ID == "apt" && f.Kind != pkgmgr.KindCancelled && f.Kind != pkgmgr.KindDiskFull && f.Kind != pkgmgr.KindGuestLibs &&
		!clockWrong(f) && !p.switched && p.aptListsUsable() && p.aptMainUnlisted() == "" {
		p.lg.Printf("package manager: some sources failed (%s); going on with the lists apt has", f.Detail)
		r.Line("Some package sources didn't answer; going on with the others.")
		p.t.Run(ctx, "dpkg -s ca-certificates >/dev/null 2>&1 || "+p.fam.Install([]string{"ca-certificates"}), nil, nil)
		return nil
	}
	return f
}

// halfInstalled lists the packages dpkg left half-installed, space-separated.
func (p *pkgOps) halfInstalled(ctx context.Context) string {
	var out []string
	p.t.Run(ctx, halfInstalledQuery, nil, func(l string) {
		if l = strings.TrimSpace(l); l != "" && !strings.ContainsAny(l, " \t") {
			out = append(out, l)
		}
	})
	return strings.Join(out, " ")
}

// prootSignal11 reports whether proot logged a SIGSEGV ("signal 11") in the
// captured output — the crash class in #319 on GKI 6.6/6.12 kernels.
func prootSignal11(lines []string) bool {
	for _, l := range lines {
		if strings.Contains(l, "proot") && strings.Contains(l, "signal 11") {
			return true
		}
	}
	return false
}

// tryL2SOff is the #319 experiment (opt-in via ANDRONIX_L2S_RETRY=1): when a
// dpkg op SIGSEGV'd proot, retry once with --link2symlink dropped, to test
// whether its hardlink emulation is the cause. Fires at most once a run and
// only on the proot-SIGSEGV dpkg class, so it leaves every other path (and
// the #312 seccomp experiment) untouched. Off by default: without
// --link2symlink dpkg's hardlinks go straight through and may fail.
func (p *pkgOps) tryL2SOff(f *pkgmgr.Failure) bool {
	if p.l2sTried || proot.NoLink2Symlink || !p.prootSegv || os.Getenv("ANDRONIX_L2S_RETRY") != "1" {
		return false
	}
	switch f.Kind {
	case pkgmgr.KindDpkg, pkgmgr.KindReinstreq, pkgmgr.KindProotCrash:
	default:
		return false
	}
	proot.NoLink2Symlink = true
	p.l2sTried = true
	p.l2sRetry = f.Kind
	p.lg.Printf("package manager: %s with proot SIGSEGV (%s); trying again with --link2symlink off", f.Kind, f.Detail)
	return true
}

// recover prepares the next try for this failure (the n-th of its kind),
// or reports that trying again won't help.
func (p *pkgOps) recover(ctx context.Context, f *pkgmgr.Failure, n int, isUpdate bool, r ui.Reporter) bool {
	apt := p.fam.ID == "apt"
	// An apk refresh that failed after the server answered (an HTTP error, a
	// download that stalled) wasn't a dead network: apk's own timeout from
	// here on, as before 2.1.6. A failed lookup says nothing either way.
	if p.fam.ID == "apk" && isUpdate && f.Kind != pkgmgr.KindConnect && f.Kind != pkgmgr.KindDNS {
		p.apkSecs = 0
	}
	sh := func(cmd string) { p.t.Run(ctx, cmd, nil, r.Line) }
	refresh := func() {
		if !isUpdate {
			p.t.Run(ctx, p.fam.Update, nil, r.Line)
		}
	}
	// #319 experiment: a dpkg op that SIGSEGV'd proot gets one retry with
	// --link2symlink dropped, ahead of the normal dpkg recovery (which itself
	// crashes under proot here). Opt-in and naturally scoped by prootSegv.
	if p.tryL2SOff(f) {
		return true
	}
	// A signature check failing on a phone whose clock is far behind: no
	// mirror or retry fixes that, so stop and say to fix the date (#354).
	// (apt's own "not valid yet" date check keeps its one retry with the
	// date checks off below: that can work when only the Release is newer.)
	if f.Signature() && f.Kind != pkgmgr.KindClock {
		if now, behind := clockBehind(); behind {
			p.lg.Printf("package manager: %s (%s) with the phone's clock at %s, before this installer was built; not retrying", f.Kind, f.Detail, now.UTC().Format(time.RFC3339))
			f.Kind = pkgmgr.KindClock
			f.Detail = clockDetail(now)
			return false
		}
	}
	switch f.Kind {
	case pkgmgr.KindCancelled, pkgmgr.KindDiskFull:
		return false
	case pkgmgr.KindDNS, pkgmgr.KindConnect, pkgmgr.KindHTTP5xx:
		// A distro without the session's DNS forwarder (Alpine: musl has
		// no NSS) asks only resolv.conf's public servers. Where Android's
		// resolver answers and they don't, use its addresses for the
		// package servers and try again at once (card #398).
		if f.Kind == pkgmgr.KindDNS && p.pinPkgHosts(ctx, r) {
			return true
		}
		// apk's refresh waits out a timeout for every address of every
		// source it can't connect to. With the servers not answering the
		// installer either, the next try gets a short one, and the tries'
		// number and the time they may start in are capped; with a server
		// that answers, the link is slow, and apk gets the timeout it has
		// always had (see apkRefreshTimeout).
		if p.fam.ID == "apk" && isUpdate && f.Kind == pkgmgr.KindConnect {
			// The last of the short tries, after servers that didn't
			// answer: nothing more to ask.
			if p.apkSecs == apkRetryTimeout && n >= apkConnectTries {
				p.lg.Printf("package manager: the package servers still can't be connected to; not trying again")
				return false
			}
			answers := apkServersAnswer(ctx, p.t.Rootfs)
			if ctx.Err() != nil {
				return false // cancelled while asking: no pause, no retry
			}
			if answers {
				p.lg.Printf("package manager: a package server accepts connections; the next try has apk's own timeout")
				p.apkSecs = 0
			} else {
				p.apkSecs = apkRetryTimeout
				if n >= apkConnectTries || p.now().Sub(p.began) >= apkRefreshBudget {
					p.lg.Printf("package manager: the package servers don't accept connections; not trying again")
					return false
				}
			}
		}
		if n == 1 {
			p.sleep(5 * time.Second)
			return true
		}
		if p.switchMirror(ctx, r, isUpdate) {
			// Retry on the new mirror only if its index re-synced; a refresh
			// that failed too means the network is down, so stop with the
			// connect/DNS failure rather than a misleading not-found (#341).
			return !p.indexStale
		}
		// apk doesn't stop at the first package it can't fetch: every one
		// left waits out its own timeout (5 s for DNS, 60 s for each address
		// it can't connect to), so
		// one run of a big install on a dead network is long already.
		// Outside the refresh it gets the one retry it had before 2.1.6.
		if p.fam.ID == "apk" && !isUpdate {
			return false
		}
		if n <= 3 {
			p.sleep(time.Duration(n*10) * time.Second)
			return true
		}
		return false
	case pkgmgr.KindHTTP404:
		if n > 2 {
			return false
		}
		if n == 2 {
			if !p.switchMirror(ctx, r, isUpdate) {
				return false
			}
			return !p.indexStale // #341: re-synced the new mirror, else stop
		}
		refresh() // n==1: stale lists name packages the mirror replaced
		return true
	case pkgmgr.KindHash:
		if n > 2 {
			return false
		}
		// pacman: a package fails its PGP signature check. First make the
		// local keyring whole again (no keyserver): an earlier run stopped
		// during pacman-key leaves keys nothing trusts, which reads as
		// exactly this failure (support #5349). Then refresh the keyring
		// packages once more, as in "Preparing ... for updates" (or that
		// step was skipped), and retry (#354/#283).
		if p.fam.ID == "pacman" && f.Signature() && !p.keyringRetried && p.keyringRebuild+p.keyringRefresh != "" {
			p.keyringRetried = true
			p.lg.Printf("package manager: PGP signature failure (%s); rebuilding the keyring's trust and refreshing the keyring packages", f.Detail)
			r.Line("Refreshing the package signing keys...")
			for _, c := range []string{p.keyringRebuild, p.keyringRefresh} {
				if c != "" {
					sh(c)
				}
			}
		}
		if apt {
			sh("rm -rf /var/lib/apt/lists/partial/* /var/lib/apt/lists/*_* /var/cache/apt/archives/partial/*; apt-get clean")
		}
		if n == 2 {
			if p.switchMirror(ctx, r, isUpdate) {
				return !p.indexStale // #341: re-synced the new mirror, else stop
			}
			if !apt {
				return false
			}
			// apt with no other mirror: refetch the same mirror after the clean.
		}
		refresh()
		return true
	case pkgmgr.KindClock:
		if !apt || n > 1 {
			return false
		}
		p.lg.Printf("package manager: the phone's clock looks wrong; apt stops checking release dates")
		pkgmgr.WriteFile(p.t.Rootfs, "etc/apt/apt.conf.d/91andronix-clock",
			"// Andronix: the phone's clock was off; don't fail on release dates.\nAcquire::Check-Valid-Until \"false\";\nAcquire::Check-Date \"false\";\n")
		refresh()
		return true
	case pkgmgr.KindDpkg:
		if !apt || n > 2 {
			return false
		}
		sh("dpkg --configure -a")
		return true
	case pkgmgr.KindPacmanDB:
		// A killed pacman left a package without its database entry's
		// desc/files (support #5412): reinstall those, once, then retry.
		if p.fam.ID != "pacman" || p.fam.Repair == "" || n > 1 {
			return false
		}
		p.lg.Printf("package manager: pacman's local database has half-installed packages (%s); reinstalling them", f.Detail)
		sh(p.fam.Repair)
		return true
	case pkgmgr.KindReinstreq:
		if !apt || n > 2 {
			return false
		}
		p.lg.Printf("package manager: dpkg has half-installed packages; reinstalling them")
		sh(reinstreqFix)
		return true
	case pkgmgr.KindFakeRoot, pkgmgr.KindProotSyscall:
		// Once, with proot on ptrace only for the rest of this run: the
		// experiment for #312 (whether the retry fixed it is logged and
		// sent as fake_root_retried / fake_root_not_fixed).
		if n > 1 || proot.NoSeccomp {
			return false
		}
		proot.NoSeccomp = true
		p.seccompRetry = f.Kind
		p.lg.Printf("package manager: %s under proot (%s); trying again with PROOT_NO_SECCOMP=1", f.Kind, f.Detail)
		return true
	case pkgmgr.KindLock:
		if n > 2 {
			return false
		}
		// An earlier run of this distro still holding it (support #296):
		// wait for it, or stop it; then finish what dpkg was doing.
		if p.waitOrStopHolder(ctx, f.Line, r) {
			if apt {
				sh("dpkg --configure -a")
			}
			if p.fam.ID == "pacman" && findLockHolder(p.t.Rootfs, "") == nil {
				sh("rm -f /var/lib/pacman/db.lck")
			}
			return true
		}
		if p.liveHolder != 0 {
			f.Detail = fmt.Sprintf("another andronix (process %d) is using it", p.liveHolder)
			return false
		}
		// pacman leaves db.lck behind when it's killed; only remove it with
		// no pacman of this distro running.
		if p.fam.ID == "pacman" && findLockHolder(p.t.Rootfs, "") == nil {
			sh("rm -f /var/lib/pacman/db.lck")
		}
		p.sleep(10 * time.Second)
		return true
	case pkgmgr.KindBroken:
		if !apt || n > 2 {
			return false
		}
		if n == 1 {
			sh(aptFix)
			return true
		}
		// Still unmet on a fallback mirror: its index is older than the
		// packages this install already has from the distro's own server
		// (support #5242). Back to that server, once, with a fresh index.
		if !p.backToPrimary(ctx, r, isUpdate) {
			return false
		}
		return !p.indexStale
	case pkgmgr.KindHook:
		// An optional apt hook failed (command-not-found's database and
		// the like): clear them and try again (support #83 #85 #87).
		if !apt || n > 2 {
			return false
		}
		if n == 1 {
			if off := disableFailingHooks(p.t.Rootfs, f.Line); len(off) > 0 {
				p.lg.Printf("package manager: an apt hook failed; turned off %s", strings.Join(off, ", "))
				r.Line("Turned off apt's " + strings.Join(off, ", ") + " hook (it fails under proot); to turn it back on, see /etc/andronix/apt-hooks-disabled.txt")
				return true
			}
			p.lg.Printf("package manager: an apt hook failed (%s), but none of apt's hook files matches it", f.Detail)
		}
		// A refresh: once more with apt's update hooks cleared for this
		// run only (support #147: Ubuntu 26.04, no lists at all, so not a
		// Post-Invoke hook after them). The hooks are logged first. A
		// rescued install says so in telemetry (apt_hook_retried, lead).
		if !isUpdate || p.noUpdateHooks {
			return false
		}
		p.t.Run(ctx, "apt-config dump 2>/dev/null | grep -i -A3 'Update::.*Invoke'", nil, func(l string) { p.lg.Printf("apt hooks: %s", l) })
		pkgmgr.WriteFile(p.t.Rootfs, strings.TrimPrefix(noUpdateHooksConf, "/"), noUpdateHooksText)
		p.noUpdateHooks = true
		p.lg.Printf("package manager: refreshing again without apt's update hooks")
		r.Line("Refreshing again without apt's update hooks (one failed under proot).")
		return true
	case pkgmgr.KindMirror:
		// Once on the next mirror, a direct one (not a redirector); if it
		// happens there too, it's the network (the message says so).
		if n > 1 || !p.switchMirror(ctx, r, isUpdate) {
			return false
		}
		return !p.indexStale // #341: retry only with a fresh index for it
	case pkgmgr.KindIntercepted:
		// One other mirror, in case it's that mirror; if it happens there
		// too, it's the network (the message says so).
		if n > 1 || !p.switchMirror(ctx, r, isUpdate) {
			return false
		}
		return !p.indexStale // #341
	case pkgmgr.KindProotExec:
		return false // the same exec fails again; nothing to change from here
	case pkgmgr.KindGuestLibs:
		// Not retried (the same load fails again). Log what tells the
		// causes apart (card #377): whether distinct libraries read as the
		// same file (device:inode) under proot — the loader then merges
		// them, which fits every version reading as missing — and what is
		// preloaded. One-library programs (stat, cat) still run then.
		p.t.Run(ctx, guestLibsProbe, nil, func(l string) { p.lg.Printf("guest libs: %s", l) })
		return false
	case pkgmgr.KindUnauth:
		// Packages from lists apt couldn't verify (#354, #4213): fetch the
		// lists again, once; if that refresh fails its checks too, stop with
		// this error rather than install unverified packages.
		if !apt || n > 1 {
			return false
		}
		p.lg.Printf("package manager: unauthenticated packages (%s); refreshing the package lists", f.Detail)
		if err := p.t.Run(ctx, p.fam.Update, nil, r.Line); err != nil {
			p.lg.Printf("package manager: the refresh failed too: %v", err)
			return false
		}
		return true
	case pkgmgr.KindNotFound:
		// apt only (do). pack add stops at the first error of a package run
		// that it can't name a recovery for, as it always has.
		if p.shared {
			return false
		}
		if isUpdate {
			// The refresh's own ca-certificates install, after an apt-get
			// update that exited 0: the retried command is the refresh. Once
			// more, then on the next mirror when the main archive still has
			// no lists (do checks that the mirror's arrived: switchedForNothing).
			if n == 1 {
				return true
			}
			if n > 2 || p.aptNoLists() == "" {
				return false
			}
			p.moveOnly = map[int]bool{0: true}
			if !p.switchMirror(ctx, r, isUpdate) {
				if ctx.Err() != nil {
					f.Kind = pkgmgr.KindCancelled // cancelled while a mirror was asked
				}
				return false
			}
			return true
		}
		return p.relist(ctx, f, r)
	case pkgmgr.KindProotCrash:
		// proot SIGSEGV'd; retrying the same proot just crashes again and
		// loops on a half-done package op. Stop now and let the user message
		// route to the app's bundled proot / a Termux update (card #319).
		// (The opt-in --link2symlink-off experiment above may still fire.)
		p.lg.Printf("package manager: proot crashed (%s); not retrying, the proot build can't run this op on this phone", f.Detail)
		return false
	case pkgmgr.KindProotArgs:
		if n > 1 {
			return false
		}
		p.lg.Printf("package manager: proot refused an option (%s); trying with only the essential ones", proot.Describe())
		proot.Minimal = true
		return true
	case pkgmgr.KindKeyring:
		// Once: a clean keyring with gpg-agent started by us, its own
		// errors and socket paths logged, so a phone where it still won't
		// start tells us why (support #119).
		if n > 1 {
			return false
		}
		p.t.Run(ctx, keyringReset, nil, func(l string) { p.lg.Printf("keyring: %s", l) })
		p.keyringAgent = true
		return true
	case pkgmgr.KindGPG:
		// A mirror in the middle of a sync, once.
		if n > 1 || !p.switchMirror(ctx, r, isUpdate) {
			return false
		}
		return !p.indexStale // #341
	}
	// killed (Android stopped it) and anything else: once more. Not for pack
	// add: apk reports a package whose script failed as installed the second
	// time, and pack has always stopped at the first error it can't name.
	if p.shared && f.Kind == pkgmgr.KindOther {
		return false
	}
	return n == 1
}

// guestLibsProbe prints the identity of the libraries apt needs and the
// preload list, for the install log (card #377).
const guestLibsProbe = `for f in /lib/*-linux-gnu*/libc.so.6 /lib/*-linux-gnu*/libstdc++.so.6 /lib/*-linux-gnu*/libgcc_s.so.1 /lib/*-linux-gnu*/libapt-pkg.so.* /lib/*-linux-gnu*/libapt-private.so.*; do [ -e "$f" ] && stat -L -c '%d:%i %s %n' "$f"; done 2>&1 | head -n 12; echo "ld.so.preload: $(cat /etc/ld.so.preload 2>/dev/null | tr '\n' ' ')"; uname -rm`

// keyringReset clears a half-made pacman keyring (the retried Update
// makes the keys again) and reports where gpg wants its sockets. A keyring
// with keys pacman already trusts stays: it verifies packages as it is (an
// install from before 2.1.6 whose one-time --populate failed), and the
// retried Update's pacman-key steps are safe to run on it again.
const keyringReset = `G=/etc/pacman.d/gnupg
gpgconf --homedir "$G" --kill all 2>/dev/null
if ` + pkgmgr.PacmanKeyringTrusted + `; then echo "kept: it has trusted keys"; else rm -rf "$G" && mkdir -p "$G" && chmod 700 "$G"; fi
echo "socketdir: $(gpgconf --homedir "$G" --list-dirs socketdir 2>&1)"`

// keyringAgentLog is gpg-agent's own log for the retried runs.
const keyringAgentLog = "/tmp/andronix-gpg-agent.log"

// keyringAgentWrap runs cmd with a gpg-agent we started in the same proot
// session (one started in another session is gone: proot ends them with
// it). Its output goes to a file: a daemon holding our pipe would hang the
// run. Its start is capped at 20 s (lead: a misbehaving agent must never
// stall the install), and it's stopped at the end, so proot never waits
// for it.
func keyringAgentWrap(cmd string) string {
	return `G=/etc/pacman.d/gnupg; mkdir -p "$G" && chmod 700 "$G"; : >` + keyringAgentLog + `; ` +
		`T=; command -v timeout >/dev/null 2>&1 && T="timeout 20"; ` +
		`$T gpg-agent --homedir "$G" --daemon --verbose --log-file ` + keyringAgentLog + ` </dev/null >/dev/null 2>&1; ` +
		`echo "andronix: gpg-agent started: $?" >>` + keyringAgentLog + `; ` +
		`{ ` + cmd + `; }; s=$?; gpgconf --homedir "$G" --kill gpg-agent 2>/dev/null; exit $s`
}

// halfInstalledQuery prints the packages dpkg flags reinst-required (R) or
// half-installed (H), one per line.
const halfInstalledQuery = `dpkg-query -W -f='${db:Status-Abbrev}|${binary:Package}\n' 2>/dev/null | awk -F'|' 'substr($1,3,1)=="R" || substr($1,2,1)=="H" {print $2}'`

// reinstreqFix repairs packages dpkg left half-unpacked (support #201): the
// ones dpkg itself flags (reinst-required or half-installed, read from
// dpkg, not from apt's message) are reinstalled; any still flagged are
// removed, so the retry installs them afresh; then the rest is configured.
const reinstreqFix = `bad() { ` + halfInstalledQuery + `; }
p=$(bad); [ -n "$p" ] || exit 0
echo "Reinstalling half-installed packages: $p"
DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -y install --reinstall $p
p=$(bad); [ -z "$p" ] || dpkg --remove --force-remove-reinstreq --force-depends $p
dpkg --configure -a`

const aptFix = "DEBIAN_FRONTEND=noninteractive apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -y -f install"

// nextMirror points the sources at each group's next usable mirror: one that
// answers with what apt will ask it for and isn't behind the lists apt
// already has (mirrorProbe, #399). The mirrors after the current one come
// first, then the distro's own server and the ones between (for an install
// an earlier run left further down the list). A mirror that fails the check
// is passed over, and asked again by the next command; one this run already
// moved away from isn't picked again.
func (p *pkgOps) nextMirror(ctx context.Context, r ui.Reporter) bool {
	p.prepare()
	p.moves = nil
	for g, list := range p.mirrors {
		if p.moveOnly != nil && !p.moveOnly[g] {
			continue // only the main archive's server failed (aptMainMissed)
		}
		from := list[p.cur[g]]
		if !pkgmgr.SourcesUse(p.t.Rootfs, from) {
			continue // nothing to move (another CPU's sources, say)
		}
		var order []int
		for i := p.cur[g] + 1; i < len(list); i++ {
			order = append(order, i)
		}
		for i := p.home[g]; i < p.cur[g]; i++ {
			order = append(order, i)
		}
		for _, i := range order {
			to := list[i]
			if p.left[to] || p.probeFailed[to] {
				continue
			}
			if ctx.Err() != nil {
				return len(p.moves) > 0
			}
			if p.fam != nil && p.fam.ID == "apt" {
				r.Line("Checking another mirror: " + hostOf(to))
				if why := mirrorProbe(ctx, p.t.Rootfs, from, to); why != "" {
					p.lg.Printf("package manager: not switching to %s: %s", to, why)
					if p.probeFailed == nil {
						p.probeFailed = map[string]bool{}
					}
					p.probeFailed[to] = true
					continue
				}
			}
			if err := pkgmgr.RewriteBase(p.t.Rootfs, from, to); err != nil {
				p.lg.Printf("package manager: switching %s to %s: %v", from, to, err)
				break
			}
			p.moveTo(g, i)
			p.lg.Printf("package manager: switched the sources from %s to %s", from, to)
			r.Line("Switching to another mirror: " + hostOf(to))
			break
		}
	}
	return len(p.moves) > 0
}

// moveTo records that group g's sources now use its i-th server.
func (p *pkgOps) moveTo(g, i int) {
	if p.left == nil {
		p.left = map[string]bool{}
	}
	p.left[p.mirrors[g][p.cur[g]]] = true
	p.moves = append(p.moves, mirrorMove{g, p.cur[g]})
	p.cur[g] = i
}

// undoMoves points the sources back at where the last switch found them.
// Their lists are still on disk: a refresh that failed removes none.
func (p *pkgOps) undoMoves() {
	for _, m := range p.moves {
		from, to := p.mirrors[m.g][p.cur[m.g]], p.mirrors[m.g][m.was]
		if err := pkgmgr.RewriteBase(p.t.Rootfs, from, to); err != nil {
			p.lg.Printf("package manager: switching %s back to %s: %v", from, to, err)
			continue
		}
		p.cur[m.g] = m.was
		p.lg.Printf("package manager: no fresh index from %s; the sources are on %s again", from, to)
	}
	p.moves = nil
}

// backToPrimary points the sources of every group that is on a fallback
// mirror back at the distro's own server and re-syncs the index (as
// switchMirror does), once a run. Only when that server answers now
// (mirrorProbe): an install that is on a mirror because the distro's server
// can't be reached stays there. When the refresh then brings no index from
// it after all, resync puts the sources back on the mirror. False: nothing
// moved.
func (p *pkgOps) backToPrimary(ctx context.Context, r ui.Reporter, isUpdate bool) bool {
	p.prepare()
	if p.wentHome {
		return false
	}
	p.wentHome = true
	p.moves = nil
	for g, list := range p.mirrors {
		if p.cur[g] <= p.home[g] {
			continue
		}
		from, to := list[p.cur[g]], list[p.home[g]]
		if !pkgmgr.SourcesUse(p.t.Rootfs, from) {
			continue
		}
		if why := mirrorProbe(ctx, p.t.Rootfs, from, to); why != "" {
			p.lg.Printf("package manager: unmet dependencies on the fallback mirror %s, but %s can't be used now: %s", from, to, why)
			continue
		}
		if err := pkgmgr.RewriteBase(p.t.Rootfs, from, to); err != nil {
			p.lg.Printf("package manager: switching %s back to %s: %v", from, to, err)
			continue
		}
		p.moveTo(g, p.home[g])
		p.lg.Printf("package manager: unmet dependencies on the fallback mirror %s; switched the sources back to %s", from, to)
		r.Line("Going back to " + hostOf(to) + " (the other mirror may be out of date)...")
	}
	if len(p.moves) == 0 {
		return false
	}
	p.switched = true
	p.resync(ctx, r, isUpdate)
	return true
}

// switchMirror moves the sources to the next mirror and re-syncs the package
// index for it before the caller retries. #341: without the re-sync the retry
// runs against the old mirror's stale or empty lists and reports packages
// "not found" / "not in repository pool"; if the re-sync itself fails (the new
// mirror is unreachable too) it sets indexStale, so the caller stops with the
// network failure instead of the misleading not-found. On the update step the
// retried command is itself the index refresh, so only the switch is done here.
// Returns false when there is no further mirror to move to.
func (p *pkgOps) switchMirror(ctx context.Context, r ui.Reporter, isUpdate bool) bool {
	if !p.nextMirror(ctx, r) {
		return false
	}
	p.switched = true
	p.resync(ctx, r, isUpdate)
	return true
}

// resync refreshes the package index after the sources moved, and records
// in indexStale whether that failed. apt-get update exits 0 when a server
// only couldn't be reached ("W: Failed to fetch"), so for apt the new
// server's lists must also be on disk and the refresh must not have failed
// to fetch them (aptRefreshMissed). After a failed refresh the sources go
// back to where they were, so they and the lists on disk still match: what
// runs next gets the old server's failure, not "Unable to locate package".
// On the update step the retried command is the refresh, so there is
// nothing to do here: do makes the same check when that refresh exits 0
// (switchedForNothing).
func (p *pkgOps) resync(ctx context.Context, r ui.Reporter, isUpdate bool) {
	p.indexStale = false
	if isUpdate {
		return
	}
	p.lg.Printf("package manager: refreshing the package index for the new mirror")
	r.Line("Refreshing the package index for the new mirror...")
	var out []string
	err := p.t.Run(ctx, p.fam.Update, nil, func(l string) {
		if len(out) < 500 {
			out = append(out, l)
		}
		r.Line(l)
	})
	if err != nil {
		p.lg.Printf("package manager: couldn't refresh the index after the mirror switch: %v", err)
		p.indexStale = true
	} else if p.fam.ID == "apt" {
		for _, m := range p.moves {
			if why := aptRefreshMissed(p.t.Rootfs, p.mirrors[m.g][p.cur[m.g]], out); why != "" {
				p.lg.Printf("package manager: %s", why)
				p.indexStale = true
			}
		}
	}
	if p.indexStale {
		p.undoMoves()
	}
}

// switchedForNothing is resync's check for apt's refresh step, where the
// retried command is itself the refresh for the new mirror: that refresh
// exited 0 (apt-get update does when a server only couldn't be reached), yet
// a group whose sources moved during the step (from: where each stood when
// it began) has no lists from its new server, or apt said it failed to fetch
// them (unfetched: its "Failed to fetch" lines). The sources then go back to
// where the step found them, as after a failed resync, so they and the lists
// on disk still match (#341), and the step ends with the failure that led to
// the switch.
func (p *pkgOps) switchedForNothing(from []int, unfetched []string) bool {
	var moved []mirrorMove
	missed := false
	for g := range p.mirrors {
		if g >= len(from) || p.cur[g] == from[g] {
			continue
		}
		moved = append(moved, mirrorMove{g, from[g]})
		if why := aptRefreshMissed(p.t.Rootfs, p.mirrors[g][p.cur[g]], unfetched); why != "" {
			p.lg.Printf("package manager: %s", why)
			missed = true
		}
	}
	if !missed {
		return false
	}
	p.moves = moved
	p.undoMoves()
	return true
}

// aptRefreshMissed says why a refresh that exited 0 still brought no index
// from base, "" when it did: apt has no Release file on disk for the first
// suite the sources take from base, or the refresh said it failed to fetch
// that suite's. With no suite to go by it has no objection.
func aptRefreshMissed(rootfs, base string, out []string) string {
	suites := pkgmgr.AptSuites(rootfs, base)
	if len(suites) == 0 {
		return ""
	}
	if pkgmgr.AptListDate(rootfs, base, suites[0].Name).IsZero() {
		return "the refresh left no package lists for " + base
	}
	dists := strings.TrimSuffix(base, "/") + "/dists/" + suites[0].Name + "/"
	for _, l := range out {
		if strings.Contains(l, "Failed to fetch") && strings.Contains(l, dists) {
			return "the refresh didn't reach " + base + " (apt went on with older lists)"
		}
	}
	return ""
}

// aptNoLists says why apt can't know the main archive's packages, "" when it
// can: it has no package lists at all, no Release file from the server the
// sources use for the main archive (aptMainUnlisted), or that file without a
// package index next to it. apt keeps the two together, so the last is rare;
// it reads as "no installation candidate" all the same.
func (p *pkgOps) aptNoLists() string {
	if u := p.aptMainUnlisted(); u != "" {
		return "apt has no package lists from " + u
	}
	if !p.aptListsUsable() {
		return "apt has no package lists"
	}
	if base, ok := p.mainBase(); ok {
		if s := pkgmgr.AptSuites(p.t.Rootfs, base); len(s) > 0 && !pkgmgr.AptHasIndex(p.t.Rootfs, base, s[0].Name) {
			return "apt has no package index from " + strings.TrimSuffix(base, "/") + "/dists/" + s[0].Name + "/"
		}
	}
	return ""
}

// mainBase is the server the sources use for the main archive, when it is
// one of the distro's mirror list (its first group).
func (p *pkgOps) mainBase() (string, bool) {
	if len(p.mirrors) == 0 {
		return "", false
	}
	base := p.mirrors[0][p.cur[0]]
	return base, pkgmgr.SourcesUse(p.t.Rootfs, base)
}

// relist is the recovery for apt finding no version of a package
// (package_not_found) while it installs. For a package every archive has
// ("Package adduser has no installation candidate" at "Installing the
// basics") the package isn't what is missing: apt has no lists from the
// server the sources name, after a refresh that passed (auto-ticket #1092,
// support #5586 #5593 #5783: 37 reports in Nov 2026). Trying the install
// again, as before, fails the same way within seconds.
//
// So the lists are fetched again, once a run. With the main archive's lists
// on disk after that, the install is tried again; a package that still isn't
// found is a missing package, and the sources stay where they are. Without
// them, the main archive moves to the next mirror, which counts only when
// its lists arrived (switchMirror, resync). When no mirror's did, the
// failure is the refresh's: the network error, not "no installation
// candidate".
func (p *pkgOps) relist(ctx context.Context, f *pkgmgr.Failure, r ui.Reporter) bool {
	if p.relisted {
		// A second command of this run (the basics again without the app's
		// display packages): the lists aren't fetched a second time.
		if p.noLists != nil {
			*f = *p.noLists
		}
		return false
	}
	p.relisted = true
	p.lg.Printf("package manager: apt found no such package (%s); refreshing the package lists", f.Detail)
	r.Line("A package wasn't found; refreshing the package lists...")
	var out []string
	err := p.t.Run(ctx, p.fam.Update, nil, func(l string) {
		if len(out) < 500 {
			out = append(out, l)
		}
		r.Line(l)
	})
	if ctx.Err() != nil {
		f.Kind = pkgmgr.KindCancelled
		return false
	}
	why := p.aptNoLists()
	if base, ok := p.mainBase(); why == "" && ok {
		// Older lists on disk, and the refresh said it didn't reach the server.
		why = aptRefreshMissed(p.t.Rootfs, base, out)
	}
	if why == "" {
		if err != nil {
			// Another source's failure; the main archive's lists are there.
			p.lg.Printf("package manager: the refresh failed (%v), with the main archive's package lists on disk", err)
		}
		p.relistRetry = true
		return true
	}
	p.lg.Printf("package manager: %s after the refresh", why)
	var rf *pkgmgr.Failure
	if err != nil {
		rf = pkgmgr.Diagnose(out, err)
	}
	// A full disk isn't a mirror's fault.
	if rf == nil || rf.Kind != pkgmgr.KindDiskFull {
		p.moveOnly = map[int]bool{0: true} // the main archive's server only
		if p.switchMirror(ctx, r, false) && !p.indexStale {
			p.relistRetry = true
			return true
		}
		if ctx.Err() != nil {
			f.Kind = pkgmgr.KindCancelled
			return false
		}
	}
	p.listsFailure(f, rf, out)
	p.noLists = new(pkgmgr.Failure)
	*p.noLists = *f
	return false
}

// listsFailure makes f, apt's "no such package", the failure behind it: the
// package lists that never arrived. rf is the failed refresh, diagnosed (nil
// when it exited 0), out its output. What the refresh itself said wins when
// it is about the server or what it sent (a lookup or connection failure, a
// mirror without its index, a signature, a full disk, Android stopping it).
// Another failure of the refresh keeps its line as "other": its kind's own
// recovery never ran here, and the error for that kind would say it had. A
// refresh that named nothing is a server that wasn't reached, which is the
// only failure apt lets pass without an error.
func (p *pkgOps) listsFailure(f, rf *pkgmgr.Failure, out []string) {
	was := f.Detail
	failed := rf != nil
	if rf == nil && len(out) > 0 {
		rf = pkgmgr.Diagnose(out, errMainMissed)
	}
	switch {
	case rf != nil && listsKind(rf.Kind):
		f.Kind, f.Detail, f.Line = rf.Kind, rf.Detail, rf.Line
	case failed && rf.Kind != pkgmgr.KindNotFound:
		f.Kind, f.Detail, f.Line = pkgmgr.KindOther, rf.Detail, rf.Line
	default:
		from := "the distro's package server"
		if base, ok := p.mainBase(); ok {
			from = hostOf(base)
		}
		f.Kind, f.Line = pkgmgr.KindConnect, ""
		f.Detail = pkgmgr.CleanDetail("no package lists from " + from)
	}
	f.Lists = true
	p.lg.Printf("package manager: %q is the package lists that didn't arrive, on any mirror tried: %s: %s", was, f.Kind, f.Detail)
}

// listsKind: a failed refresh of this kind is why the lists didn't arrive,
// and its error reads right without the kind's own retries (listsFailure).
func listsKind(k pkgmgr.Kind) bool {
	switch k {
	case pkgmgr.KindHTTP404, pkgmgr.KindMirror, pkgmgr.KindIntercepted, pkgmgr.KindGPG, pkgmgr.KindHash,
		pkgmgr.KindClock, pkgmgr.KindDiskFull, pkgmgr.KindKilled:
		return true
	}
	return k.Network()
}

// errMainMissed stands in for the exit status of an apt refresh that exited
// 0 without lists from the main archive (aptMainMissed).
var errMainMissed = errors.New("the refresh left the distro's main archive without package lists")

// aptMainUnlisted is the main archive's first suite on the server the
// sources use for it (".../dists/<suite>/") when apt has no lists from it: no
// Release file on disk. "" when it has, when the sources don't use a server
// of the distro's mirror list, or with no suite to go by. The main archive
// is DISTRO_MIRROR_FALLBACK's first group (Debian's second is the security
// archive, which an install can do without).
func (p *pkgOps) aptMainUnlisted() string {
	if len(p.mirrors) == 0 {
		return ""
	}
	base := p.mirrors[0][p.cur[0]]
	if !pkgmgr.SourcesUse(p.t.Rootfs, base) {
		return ""
	}
	s := pkgmgr.AptSuites(p.t.Rootfs, base)
	if len(s) == 0 || !pkgmgr.AptListDate(p.t.Rootfs, base, s[0].Name).IsZero() {
		return ""
	}
	return strings.TrimSuffix(base, "/") + "/dists/" + s[0].Name + "/"
}

// aptMainMissed is the check for an apt refresh that exited 0: apt's "Failed
// to fetch" line for the main archive (unfetched holds those lines) when the
// refresh left it without any lists (aptMainUnlisted), "" otherwise. With
// older lists on disk apt goes on with those, as it always has; so does a
// refresh that printed no failure for it (one that fetched nothing new).
func (p *pkgOps) aptMainMissed(unfetched []string) string {
	dists := p.aptMainUnlisted()
	if dists == "" {
		return ""
	}
	for _, l := range unfetched {
		if strings.Contains(l, dists) {
			return l
		}
	}
	return ""
}

// clockDetail names the phone's (wrong) date, for the error and telemetry.
func clockDetail(now time.Time) string {
	return "the phone's date is " + now.Format("2 January 2006") + ", which is too early"
}

// clockWrong: f is a clock failure on a phone whose clock is behind.
func clockWrong(f *pkgmgr.Failure) bool {
	_, behind := clockBehind()
	return f.Kind == pkgmgr.KindClock && behind
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "http://"), "https://")
	h, _, _ := strings.Cut(u, "/")
	return h
}

// aptListsUsable: apt has package lists from at least one source.
func (p *pkgOps) aptListsUsable() bool {
	m, _ := filepath.Glob(filepath.Join(p.t.Rootfs, "var/lib/apt/lists/*_Packages*"))
	return len(m) > 0
}

// pkgTailLines is how much of a failed run's output goes to the log;
// pkgEarlyLines how many of apk's error lines from before the last 60
// lines are kept as well.
const (
	pkgTailLines  = 15
	pkgEarlyLines = 8
)

// outputTail is the last n non-empty lines, each cut to 300 characters.
func outputTail(lines []string, n int) []string {
	var out []string
	for _, l := range lines {
		if l = strings.TrimRight(l, " \t\r"); l != "" {
			out = append(out, clip(l, 300))
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// pkgRunWatch is pkgRun that also hands every line to watch.
func pkgRunWatch(ctx context.Context, t *proot.Target, f *pkgmgr.Family, cmd string, n int, r ui.Reporter, watch func(string)) error {
	return pkgRun(ctx, t, f, cmd, n, lineTee{r, watch})
}

// lineTee is a Reporter whose lines also go to fn.
type lineTee struct {
	ui.Reporter
	fn func(string)
}

func (l lineTee) Line(s string) { l.fn(s); l.Reporter.Line(s) }

// knownHooks are the apt hooks that fail under proot on phones: their
// post-invoke runs need things proot or Android don't give (support).
var knownHooks = []string{"command-not-found", "cnf-update-db", "appstream", "appstreamcli"}

// disableFailingHooks turns off the apt.conf.d files that hold the hook
// that just failed: the command in apt's "Problem executing scripts <key>
// '<command>'" line, or, when apt printed only "Sub-process returned an
// error code", the known ones (command-not-found, appstream). Each file
// gets ".disabled" (apt ignores those) and a line in
// /etc/andronix/apt-hooks-disabled.txt says how to turn it back on. Only
// files that define an update or dpkg post-invoke hook are touched.
func disableFailingHooks(rootfs, line string) []string {
	dir := filepath.Join(rootfs, "etc/apt/apt.conf.d")
	ents, _ := os.ReadDir(dir)
	needle := ""
	if i := strings.Index(line, "'"); i >= 0 {
		if j := strings.LastIndex(line, "'"); j > i {
			needle = line[i+1 : j]
		}
	}
	var off []string
	for _, e := range ents {
		name := e.Name()
		if e.IsDir() || strings.HasSuffix(name, ".disabled") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		s := string(b)
		if !strings.Contains(s, "Post-Invoke") {
			continue
		}
		match := false
		if needle != "" {
			// apt prints the command as the file has it; match on a
			// distinctive part (the first 40 characters).
			n := needle
			if len(n) > 40 {
				n = n[:40]
			}
			match = strings.Contains(s, n)
		} else {
			for _, k := range knownHooks {
				if strings.Contains(s, k) {
					match = true
					break
				}
			}
		}
		if !match {
			continue
		}
		if os.Rename(filepath.Join(dir, name), filepath.Join(dir, name+".disabled")) == nil {
			off = append(off, name)
		}
	}
	if len(off) > 0 {
		note := filepath.Join(rootfs, "etc/andronix/apt-hooks-disabled.txt")
		os.MkdirAll(filepath.Dir(note), 0o755)
		f, err := os.OpenFile(note, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err == nil {
			for _, n := range off {
				fmt.Fprintf(f, "%s: Andronix turned off /etc/apt/apt.conf.d/%s (its hook failed under proot). To turn it back on: sudo mv /etc/apt/apt.conf.d/%s.disabled /etc/apt/apt.conf.d/%s\n",
					time.Now().Format("2006-01-02"), n, n, n)
			}
			f.Close()
		}
	}
	return off
}
