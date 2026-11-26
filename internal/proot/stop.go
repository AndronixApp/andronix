package proot

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/termux"
)

// Stopping a proot run, and the processes an earlier stopped run left.
//
// proot ends its processes only when it exits on its own (--kill-on-exit).
// Killed, it leaves them running without their tracer, and such a process
// is no use to anyone: its seccomp filter still hands every file, exec and
// wait call to a tracer that is gone, so the kernel answers ENOSYS. A shell
// then spins in wait() with our output pipe open, and a gpg-agent (a daemon
// in its own session, so no signal to a process group reaches it) keeps
// accepting connections it can't serve: the next pacman-key --init waits
// for it for good (2.1.6 device gates, bugs 1 and 2).
//
// So a cancelled Run stops proot's processes first and proot last
// (stopSession), and a Run starts by ending what a killed run of the same
// distro left behind (killOrphans): the app's force-stop of the installer,
// Android's low-memory killer, an older andronix.

var (
	procDir  = "/proc" // a variable for tests
	killProc = func(pid int, sig syscall.Signal) error { return syscall.Kill(pid, sig) }
	// waitDelay is how long Run waits for more output once the command has
	// ended or was cancelled; drainMax is how long it waits for output in
	// all after that end, and drainBytes how much it reads (a pipe holds
	// 64 KB, 1 MB at the very most; see outPipe).
	waitDelay  = 5 * time.Second
	drainMax   = 30 * time.Second
	drainBytes = 2 << 20
	// sweepPause is the time between two looks for tracees in stopSession.
	sweepPause = 5 * time.Millisecond
)

// procStatus reads a process's state letter and its tracer and parent pids.
// ok is false for a process that is gone.
func procStatus(pid int) (state string, tracer, ppid int, ok bool) {
	b, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "status"))
	if err != nil {
		return "", 0, 0, false
	}
	tracer, ppid = -1, -1
	for _, l := range strings.Split(string(b), "\n") {
		k, v, found := strings.Cut(l, ":")
		if !found {
			continue
		}
		v = strings.TrimSpace(v)
		switch k {
		case "State":
			if v != "" {
				state = v[:1]
			}
		case "TracerPid":
			tracer, _ = strconv.Atoi(v)
		case "PPid":
			ppid, _ = strconv.Atoi(v)
		}
	}
	return state, tracer, ppid, state != "" && tracer >= 0
}

// pids lists the processes /proc shows (on Android: this app's own).
func pids() []int {
	es, _ := os.ReadDir(procDir)
	var out []int
	for _, e := range es {
		if n, err := strconv.Atoi(e.Name()); err == nil && n > 1 {
			out = append(out, n)
		}
	}
	return out
}

// Tracees lists the processes traced by tracer: everything a proot runs,
// daemons in their own session included. A zombie is listed too: it may be
// a main thread that exited with other threads still running.
func Tracees(tracer int) []int {
	if tracer <= 0 {
		return nil
	}
	var out []int
	for _, n := range pids() {
		if _, tr, _, ok := procStatus(n); ok && tr == tracer {
			out = append(out, n)
		}
	}
	return out
}

// stopSession ends a proot (pid, signalled through signal) and everything
// it runs. proot is frozen first: a process it traces that forks then stops
// (as does the child) until proot answers, so the sweeps below see every
// process there is and none slips out between a look and proot's end.
// It returns os.ErrProcessDone when proot had ended already.
func stopSession(pid int, signal func(syscall.Signal) error) error {
	if pid <= 1 {
		return os.ErrProcessDone
	}
	if err := signal(syscall.SIGSTOP); err != nil {
		return err
	}
	killed := map[int]bool{}
	for round := 0; round < 50; round++ {
		n := 0
		for _, t := range Tracees(pid) {
			if !killed[t] {
				killed[t] = true
				n++
				killProc(t, syscall.SIGKILL)
			}
		}
		// A look that found nothing new: nothing was born meanwhile.
		if n == 0 && round > 0 {
			break
		}
		time.Sleep(sweepPause)
	}
	return signal(syscall.SIGKILL)
}

// rootfsPrefixes are the path prefixes a file of rootfs has in
// /proc/<pid>/maps: as given, and with symlinks resolved (/data/user/0 and
// /data/data are one folder).
func rootfsPrefixes(rootfs string) []string {
	rootfs = filepath.Clean(rootfs)
	if rootfs == "" || rootfs == "/" || rootfs == "." {
		return nil
	}
	out := []string{rootfs + "/"}
	if r, err := filepath.EvalSymlinks(rootfs); err == nil && r != rootfs && r != "/" {
		out = append(out, r+"/")
	}
	return out
}

// runsFrom reports whether pid executes code of the distro in rootfs: an
// executable mapping of one of its files. Only a program started inside the
// distro has one (proot's loader maps the program and its libraries from
// the rootfs); Termux's own programs, proot and andronix never do.
func runsFrom(pid int, prefixes []string) bool {
	b, err := os.ReadFile(filepath.Join(procDir, strconv.Itoa(pid), "maps"))
	if err != nil {
		return false
	}
	for _, l := range strings.Split(string(b), "\n") {
		// address perms offset dev inode path
		f := strings.Fields(l)
		if len(f) < 6 || !strings.Contains(f[1], "x") {
			continue
		}
		i := strings.Index(l, f[5])
		if i < 0 {
			continue
		}
		for _, p := range prefixes {
			if strings.HasPrefix(l[i:], p) {
				return true
			}
		}
	}
	return false
}

// killOrphans ends the distro's processes that have no tracer: what a
// killed proot left (see the top of this file). A process under a live
// proot, in this session or any other (a login shell, the desktop), always
// has its tracer and is never touched. It returns the pids it ended.
func killOrphans(rootfs string) []int {
	prefixes := rootfsPrefixes(rootfs)
	if len(prefixes) == 0 {
		return nil
	}
	self := os.Getpid()
	var out []int
	for _, n := range pids() {
		if n == self {
			continue
		}
		if _, tr, _, ok := procStatus(n); !ok || tr != 0 || !runsFrom(n, prefixes) {
			continue
		}
		if killProc(n, syscall.SIGKILL) == nil {
			out = append(out, n)
		}
	}
	return out
}

var (
	orphansMu   sync.Mutex
	orphansDone = map[string]bool{}
)

// reapOrphans runs the sweep once per rootfs in this process, before its
// first command there: what an earlier, killed andronix left.
func (t *Target) reapOrphans() {
	if t.Local || t.Rootfs == "" {
		return
	}
	orphansMu.Lock()
	done := orphansDone[t.Rootfs]
	orphansDone[t.Rootfs] = true
	orphansMu.Unlock()
	if !done {
		t.sweepOrphans("an earlier, killed run left")
	}
}

// sweepOrphans ends the distro's processes that have no tracer and logs
// them; which is how they came to be, for the log.
func (t *Target) sweepOrphans(which string) {
	if t.Local || t.Rootfs == "" {
		return
	}
	if ps := killOrphans(t.Rootfs); len(ps) > 0 {
		termux.Logf("proot: ended %d process(es) %s in %s: %v", len(ps), which, t.Rootfs, ps)
	}
}
