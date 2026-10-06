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
}

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
		err := pkgRunWatch(ctx, p.t, p.fam, cmd, n, r, func(l string) {
			if lines = append(lines, l); len(lines) > 60 {
				lines = lines[1:]
			}
		})
		if err == nil {
			if attempt > 1 {
				p.lg.Printf("package manager: worked on attempt %d", attempt)
			}
			return nil
		}
		f = pkgmgr.Diagnose(lines, err)
		seen[f.Kind]++
		p.lg.Printf("package manager: attempt %d failed: %s: %s", attempt, f.Kind, f.Detail)
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
	case pkgmgr.KindLock:
		if n > 2 {
			return false
		}
		if p.fam.ID == "pacman" {
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
