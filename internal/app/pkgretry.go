package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/proot"
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
	// keyringAgent: start gpg-agent ourselves inside each retried run
	// (a keyring failure, support #119).
	keyringAgent bool
	// noUpdateHooks: refresh without apt's APT::Update hooks (one failed,
	// and no hook file we know of matched it).
	noUpdateHooks bool
}

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

func newPkgOps(t *proot.Target, fam *pkgmgr.Family, d *conf.Distro, lg *Logger) *pkgOps {
	p := &pkgOps{t: t, fam: fam, lg: lg, sleep: time.Sleep}
	if d != nil {
		p.mirrors = d.MirrorFallbacks()
		p.cur = make([]int, len(p.mirrors))
	}
	return p
}

// update refreshes the package lists.
func (p *pkgOps) update(ctx context.Context, r ui.Reporter) error {
	return p.do(ctx, p.fam.Update, 0, r, true)
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
			p.mirrors[0] = append([]string{dead}, p.mirrors[0][p.cur[0]:]...)
			p.cur[0] = 0
			p.lg.Printf("package manager: test: sources point at %s", dead)
		}
	}
}

func (p *pkgOps) do(ctx context.Context, cmd string, n int, r ui.Reporter, isUpdate bool) error {
	p.prepare()
	var f *pkgmgr.Failure
	seen := map[pkgmgr.Kind]int{}
	for attempt := 1; attempt <= pkgAttempts; attempt++ {
		var lines []string
		run := cmd
		if p.keyringAgent {
			run = keyringAgentWrap(cmd)
		}
		if p.noUpdateHooks && isUpdate {
			run = withoutUpdateHooks(run)
		}
		err := pkgRunWatch(ctx, p.t, p.fam, run, n, r, func(l string) {
			if lines = append(lines, l); len(lines) > 60 {
				lines = lines[1:]
			}
		})
		if p.keyringAgent {
			p.t.Run(ctx, "tail -n 20 "+keyringAgentLog+" 2>/dev/null", nil, func(l string) { p.lg.Printf("keyring agent: %s", l) })
		}
		if err == nil {
			if attempt > 1 {
				p.lg.Printf("package manager: worked on attempt %d", attempt)
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
			return nil
		}
		f = pkgmgr.Diagnose(lines, err)
		if f.Kind == pkgmgr.KindOther && p.fam.ID == "apt" {
			// dpkg doesn't always say "very bad inconsistent state": with
			// a dependent configured first it says the half-installed
			// package "is not installed" (support #201). Ask dpkg.
			if half := p.halfInstalled(ctx); half != "" {
				f.Kind, f.Detail = pkgmgr.KindReinstreq, "half-installed: "+half
			}
		}
		seen[f.Kind]++
		p.lg.Printf("package manager: attempt %d failed: %s: %s", attempt, f.Kind, f.Detail)
		if k := p.seccompRetry; k != "" {
			p.seccompRetry = ""
			p.lg.Printf("package manager: %s: the retry with PROOT_NO_SECCOMP=1 didn't help (%s)", k, f.Kind)
			if f.Kind == k {
				f.Kind = k + "_not_fixed"
			}
		}
		if attempt == pkgAttempts || !p.recover(ctx, f, seen[f.Kind], isUpdate, r) {
			break
		}
		r.Line(fmt.Sprintf("Trying again (%s)...", strings.ReplaceAll(string(f.Kind), "_", " ")))
	}
	if isUpdate && p.fam.ID == "apt" && f.Kind != pkgmgr.KindCancelled && f.Kind != pkgmgr.KindDiskFull && p.aptListsUsable() {
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

// recover prepares the next try for this failure (the n-th of its kind),
// or reports that trying again won't help.
func (p *pkgOps) recover(ctx context.Context, f *pkgmgr.Failure, n int, isUpdate bool, r ui.Reporter) bool {
	apt := p.fam.ID == "apt"
	sh := func(cmd string) { p.t.Run(ctx, cmd, nil, r.Line) }
	refresh := func() {
		if !isUpdate {
			p.t.Run(ctx, p.fam.Update, nil, r.Line)
		}
	}
	switch f.Kind {
	case pkgmgr.KindCancelled, pkgmgr.KindDiskFull:
		return false
	case pkgmgr.KindDNS, pkgmgr.KindConnect, pkgmgr.KindHTTP5xx:
		if n == 1 {
			p.sleep(5 * time.Second)
			return true
		}
		if p.nextMirror(r) {
			refresh()
			return true
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
		if n == 2 && !p.nextMirror(r) {
			return false
		}
		refresh() // stale lists name packages the mirror replaced
		return true
	case pkgmgr.KindHash:
		if n > 2 {
			return false
		}
		if apt {
			sh("rm -rf /var/lib/apt/lists/partial/* /var/lib/apt/lists/*_* /var/cache/apt/archives/partial/*; apt-get clean")
		}
		if n == 2 && !p.nextMirror(r) && !apt {
			return false
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
		if !apt || n > 1 {
			return false
		}
		sh(aptFix)
		return true
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
		if n > 1 || !p.nextMirror(r) {
			return false
		}
		refresh()
		return true
	case pkgmgr.KindIntercepted:
		// One other mirror, in case it's that mirror; if it happens there
		// too, it's the network (the message says so).
		if n > 1 || !p.nextMirror(r) {
			return false
		}
		refresh()
		return true
	case pkgmgr.KindProotExec:
		return false // the same exec fails again; nothing to change from here
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
		if n > 1 || !p.nextMirror(r) {
			return false
		}
		refresh()
		return true
	}
	// killed (Android stopped it) and anything else: once more.
	return n == 1
}

// keyringReset clears a half-made pacman keyring (the retried Update
// makes the keys again) and reports where gpg wants its sockets.
const keyringReset = `G=/etc/pacman.d/gnupg
gpgconf --homedir "$G" --kill all 2>/dev/null
rm -rf "$G" && mkdir -p "$G" && chmod 700 "$G"
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

// nextMirror points the sources at each group's next mirror.
func (p *pkgOps) nextMirror(r ui.Reporter) bool {
	p.prepare()
	moved := false
	for g, list := range p.mirrors {
		if p.cur[g]+1 >= len(list) {
			continue
		}
		from, to := list[p.cur[g]], list[p.cur[g]+1]
		if err := pkgmgr.RewriteBase(p.t.Rootfs, from, to); err != nil {
			p.lg.Printf("package manager: switching %s to %s: %v", from, to, err)
			continue
		}
		p.cur[g]++
		moved = true
		p.lg.Printf("package manager: switched the sources from %s to %s", from, to)
		r.Line("Switching to another mirror: " + hostOf(to))
	}
	return moved
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
