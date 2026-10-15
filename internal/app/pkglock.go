package app

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// The package manager's lock held by an earlier run in the same distro
// (support #296: a resumed install failed with "Could not get lock ... held
// by process 20256 (apt-get)" because the earlier attempt's proot and apt
// were still running; proot's --kill-on-exit only fires when proot itself
// exits). Such a holder is waited for, and stopped if it doesn't finish; a
// holder that isn't ours (another distro, anything outside a proot of this
// rootfs) is never touched, and neither is one whose andronix is still
// running (support #313: a second install in another Termux session; the
// distro lock stops that up front, this covers older andronix versions):
// only an orphaned proot, whose andronix is gone, is ever stopped.

var (
	procRoot     = "/proc"         // a variable for tests
	lockWait     = 5 * time.Minute // how long an earlier run gets to finish
	lockPoll     = 2 * time.Second
	lockStopWait = 10 * time.Second
	signalProc   = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
)

var heldByRe = regexp.MustCompile(`held by process (\d+)`)

// lockHolder is a process holding the package lock and the proot it runs
// under, each with its start time, so a reused pid is never mistaken for it.
type lockHolder struct {
	pid, proot           int
	pidStart, prootStart string
	comm                 string
	andronix             int // a live andronix above the proot (0: orphaned)
}

// procInfo reads a process's command name, parent and start time.
func procInfo(pid int) (comm string, ppid int, start string, ok bool) {
	b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "stat"))
	if err != nil {
		return "", 0, "", false
	}
	s := string(b)
	i, j := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
	if i < 0 || j < i {
		return "", 0, "", false
	}
	f := strings.Fields(s[j+1:])
	if len(f) < 20 || f[0] == "Z" || f[0] == "X" {
		return "", 0, "", false
	}
	ppid, _ = strconv.Atoi(f[1])
	return s[i+1 : j], ppid, f[19], true
}

// prootOf is the nearest proot ancestor of pid (pid itself excluded) that
// runs rootfs ("-r <rootfs>" or "--rootfs=<rootfs>"), or 0.
func prootOf(pid int, rootfs string) (int, string) {
	for hops := 0; hops < 32; hops++ {
		_, ppid, _, ok := procInfo(pid)
		if !ok || ppid <= 1 {
			return 0, ""
		}
		pid = ppid
		b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
		if err != nil {
			return 0, ""
		}
		args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		if base := filepath.Base(args[0]); base != "proot" && base != "libproot.so" {
			continue
		}
		for k, a := range args {
			if (a == "-r" || a == "--rootfs") && k+1 < len(args) && filepath.Clean(args[k+1]) == filepath.Clean(rootfs) ||
				a == "--rootfs="+rootfs {
				_, _, start, _ := procInfo(pid)
				return pid, start
			}
		}
		return 0, "" // a proot of another distro
	}
	return 0, ""
}

// andronixAbove is the nearest andronix process above pid (the run that
// started that proot), or 0 when there is none: the proot is orphaned.
func andronixAbove(pid int) int {
	for hops := 0; hops < 32; hops++ {
		_, ppid, _, ok := procInfo(pid)
		if !ok || ppid <= 1 {
			return 0
		}
		pid = ppid
		b, err := os.ReadFile(filepath.Join(procRoot, strconv.Itoa(pid), "cmdline"))
		if err != nil {
			return 0
		}
		args := strings.Split(strings.TrimRight(string(b), "\x00"), "\x00")
		for _, a := range args[:min(2, len(args))] { // argv[1] when started through the linker
			if base := filepath.Base(a); strings.HasPrefix(base, "andronix") || base == "libandronix.so" {
				return pid
			}
		}
	}
	return 0
}

// findLockHolder is the process holding this distro's package lock: the pid
// apt names ("held by process N"), else any apt-get, apt, dpkg or pacman
// running under a proot of this rootfs. nil when none is ours.
func findLockHolder(rootfs, line string) *lockHolder {
	var cands []int
	if m := heldByRe.FindStringSubmatch(line); m != nil {
		n, _ := strconv.Atoi(m[1])
		cands = append(cands, n)
	} else {
		es, _ := os.ReadDir(procRoot)
		for _, e := range es {
			if n, err := strconv.Atoi(e.Name()); err == nil {
				if comm, _, _, ok := procInfo(n); ok && (comm == "apt-get" || comm == "apt" || comm == "dpkg" || comm == "pacman") {
					cands = append(cands, n)
				}
			}
		}
	}
	for _, pid := range cands {
		comm, _, start, ok := procInfo(pid)
		if !ok {
			continue
		}
		if pr, prStart := prootOf(pid, rootfs); pr != 0 {
			return &lockHolder{pid: pid, proot: pr, pidStart: start, prootStart: prStart, comm: comm, andronix: andronixAbove(pr)}
		}
	}
	return nil
}

// same: the holder and its proot are still the processes first seen.
func (h *lockHolder) same() bool {
	_, _, s1, ok1 := procInfo(h.pid)
	_, _, s2, ok2 := procInfo(h.proot)
	return ok1 && ok2 && s1 == h.pidStart && s2 == h.prootStart
}

// waitOrStopHolder waits for an earlier run of this distro's package
// manager to finish, and stops its proot if it doesn't within lockWait.
// It reports whether one was found (so the caller retries).
func (p *pkgOps) waitOrStopHolder(ctx context.Context, line string, r ui.Reporter) bool {
	h := findLockHolder(p.t.Rootfs, line)
	if h == nil {
		p.lg.Printf("package manager: the lock isn't held by an earlier run in this distro; leaving it alone")
		return false
	}
	if h.andronix != 0 {
		// Another andronix is working on this distro right now: leave it.
		p.lg.Printf("package manager: lock held by a running andronix (process %d: %s, pid %d, proot %d); not touching it", h.andronix, h.comm, h.pid, h.proot)
		p.liveHolder = h.andronix
		return false
	}
	p.lg.Printf("package manager: lock held by an earlier run (%s, pid %d, proot %d); waiting up to %v", h.comm, h.pid, h.proot, lockWait)
	r.Line("An earlier package update in this distro is still running; waiting for it to finish.")
	for waited := time.Duration(0); waited < lockWait && h.same(); waited += lockPoll {
		if ctx.Err() != nil {
			return false
		}
		p.sleep(lockPoll)
	}
	if h.same() { // still there: the same pid under the same proot, not a reused pid
		p.lg.Printf("package manager: the earlier run (%s, pid %d) didn't finish in %v; stopping its proot %d", h.comm, h.pid, lockWait, h.proot)
		r.Line("It didn't finish; stopping it.")
		signalProc(h.proot, syscall.SIGTERM) // --kill-on-exit takes apt with it
		for waited := time.Duration(0); waited < lockStopWait && h.same(); waited += lockPoll {
			p.sleep(lockPoll)
		}
		if h.same() {
			p.lg.Printf("package manager: proot %d didn't stop", h.proot)
			return false
		}
	} else {
		p.lg.Printf("package manager: the earlier run (%s, pid %d) finished", h.comm, h.pid)
	}
	return true
}
