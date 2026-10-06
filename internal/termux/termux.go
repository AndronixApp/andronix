// Package termux is the Termux side of a running desktop: PulseAudio for
// sound and the Termux:X11 display server. Everything here runs in
// Termux, never inside a distro.
package termux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// isTermux is a variable so tests can run the Termux paths off a phone.
var isTermux = sys.IsTermux

// logPath is where the Termux-side helpers record what they did
// (~/.andronix/logs/termux.log), one timestamped line per action.
func logPath() string {
	base := os.Getenv("ANDRONIX_HOME")
	if base == "" {
		base = filepath.Join(sys.Home(), ".andronix")
	}
	return filepath.Join(base, "logs", "termux.log")
}

// Logf appends one line to the Termux helpers' log. The file is cut back
// to its newer half once it passes 256 KB.
func Logf(format string, args ...any) {
	p := logPath()
	os.MkdirAll(filepath.Dir(p), 0o755)
	if st, err := os.Stat(p); err == nil && st.Size() > 256<<10 {
		if b, err := os.ReadFile(p); err == nil {
			b = b[len(b)/2:]
			if i := strings.IndexByte(string(b), '\n'); i >= 0 {
				b = b[i+1:]
			}
			os.WriteFile(p, b, 0o644)
		}
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}

// LogFile is the path of the helpers' log, for error hints.
func LogFile() string { return logPath() }

// pkgInstall installs Termux packages with apt-get (not pkg: pkg runs
// curl first, which can't start on a half-upgraded Termux), sending each
// output line to onLine. It refreshes the lists first (x11-repo adds a
// source) and installs non-interactively, keeping user-edited conffiles.
// A failure is diagnosed (pkgmgr.Diagnose) and recovered as for distros:
// dpkg --configure -a, a pause for a lock, refetched lists, a pause and
// then the next Termux mirror for network errors, and one apt-get
// full-upgrade for anything else (as pkg advises for a half-upgraded
// Termux: a new libcurl on an old OpenSSL). The error is a
// *pkgmgr.Failure, for the message and telemetry.
func pkgInstall(ctx context.Context, onLine func(string), pkgs ...string) error {
	if _, err := sys.LookPath("apt-get"); err != nil {
		return fmt.Errorf("apt-get not found")
	}
	// One package run at a time (two sessions starting together), and
	// never forever: a dead mirror can stall apt's download.
	unlock, err := pkgLock()
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, pkgTimeout)
	defer cancel()
	keep := []string{"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold"}
	install := append(append([]string{"-y"}, keep...), append([]string{"install"}, pkgs...)...)
	update := func() { termuxRun(ctx, onLine, "apt-get", "update") } // stale lists still let install try
	update()
	var f *pkgmgr.Failure
	seen := map[pkgmgr.Kind]int{}
	upgraded := false
	for attempt := 1; attempt <= 5; attempt++ {
		out, err := termuxRun(ctx, onLine, "apt-get", install...)
		if err == nil {
			return nil
		}
		f = pkgmgr.Diagnose(tailOf(out, 60), err)
		seen[f.Kind]++
		n := seen[f.Kind]
		Logf("apt-get: install %s failed (attempt %d): %s: %s", strings.Join(pkgs, " "), attempt, f.Kind, f.Detail)
		switch {
		case f.Kind == pkgmgr.KindCancelled || f.Kind == pkgmgr.KindDiskFull:
			return f
		case f.Kind == pkgmgr.KindDpkg && n <= 2:
			termuxRun(ctx, onLine, "dpkg", "--configure", "-a")
		case f.Kind == pkgmgr.KindLock && n <= 2:
			time.Sleep(10 * time.Second)
		case f.Kind.Network():
			if n == 1 {
				time.Sleep(5 * time.Second)
			} else if nextMirror() {
				update()
			} else if n <= 3 {
				time.Sleep(time.Duration(n*10) * time.Second)
			} else {
				return f
			}
		case (f.Kind == pkgmgr.KindHash || f.Kind == pkgmgr.KindHTTP404) && n <= 2:
			termuxRun(ctx, onLine, "apt-get", "clean")
			if n == 2 {
				nextMirror()
			}
			update()
		case (f.Kind == pkgmgr.KindGPG || f.Kind == pkgmgr.KindClock) && n == 1 && nextMirror():
			update()
		case !upgraded:
			upgraded = true
			Logf("apt-get: upgrading Termux's packages (apt-get full-upgrade), then retrying")
			if _, err := termuxRun(ctx, onLine, "apt-get", append(append([]string{"-y"}, keep...), "full-upgrade")...); err != nil {
				Logf("apt-get: full-upgrade: %v", err)
			}
		default:
			return f
		}
	}
	return f
}

func tailOf(s string, n int) []string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}

// termuxMirrors are Termux's main and x11 repositories on mirrors that
// answered in Oct 2026, in the order to try.
var termuxMirrors = [][2]string{
	{"https://packages-cf.termux.dev/apt/termux-main", "https://packages-cf.termux.dev/apt/termux-x11"},
	{"https://packages.termux.dev/apt/termux-main", "https://packages.termux.dev/apt/termux-x11"},
	{"https://grimler.se/termux/termux-main", "https://grimler.se/termux/termux-x11"},
	{"https://mirror.accum.se/mirror/termux.dev/apt/termux-main", "https://mirror.accum.se/mirror/termux.dev/apt/termux-x11"},
	{"https://mirror.mwt.me/termux/main", "https://mirror.mwt.me/termux/x11"},
	{"https://mirrors.tuna.tsinghua.edu.cn/termux/apt/termux-main", "https://mirrors.tuna.tsinghua.edu.cn/termux/apt/termux-x11"},
}

var mirrorsTried = map[string]bool{}

// nextMirror points Termux's sources at the next mirror not tried yet (as
// termux-change-repo would), for the current repositories only: Android
// 5 and 6 Termux has its own frozen ones.
func nextMirror() bool {
	list := filepath.Join(sys.Prefix(), "etc/apt/sources.list")
	b, err := os.ReadFile(list)
	if err != nil {
		return false
	}
	cur := ""
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 3 && f[0] == "deb" && f[2] == "stable" {
			cur = strings.TrimSuffix(f[1], "/")
		}
	}
	if cur == "" || !(strings.Contains(cur, "termux-main") || strings.HasSuffix(cur, "/termux/main")) {
		return false
	}
	mirrorsTried[cur] = true
	for _, m := range termuxMirrors {
		if mirrorsTried[m[0]] {
			continue
		}
		mirrorsTried[m[0]] = true
		if os.WriteFile(list, []byte("# Andronix switched mirrors after "+cur+" failed\ndeb "+m[0]+" stable main\n"), 0o644) != nil {
			return false
		}
		x11 := filepath.Join(sys.Prefix(), "etc/apt/sources.list.d/x11.list")
		if _, err := os.Stat(x11); err == nil {
			os.WriteFile(x11, []byte("deb "+m[1]+" x11 main\n"), 0o644)
		}
		Logf("apt-get: switched Termux's package mirror from %s to %s", cur, m[0])
		return true
	}
	return false
}

// Install is pkgInstall for other packages (the installer's proot).
func Install(ctx context.Context, onLine func(string), pkgs ...string) error {
	return pkgInstall(ctx, onLine, pkgs...)
}

// pkgTimeout bounds one pkgInstall, recovery included.
var pkgTimeout = 20 * time.Minute

// pkgLock takes ~/.andronix/pkg.lock, or fails at once if another andronix
// holds it.
func pkgLock() (func(), error) {
	p := filepath.Join(filepath.Dir(filepath.Dir(logPath())), "pkg.lock")
	os.MkdirAll(filepath.Dir(p), 0o755)
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		Logf("pkg: another andronix is installing Termux packages; not waiting")
		return nil, fmt.Errorf("another andronix is installing Termux packages right now")
	}
	return func() { syscall.Flock(int(f.Fd()), syscall.LOCK_UN); f.Close() }, nil
}

// termuxRun runs a Termux package command non-interactively, logging and
// passing on each output line, and returns the output.
func termuxRun(ctx context.Context, onLine func(string), name string, args ...string) (string, error) {
	c := sys.CommandContext(ctx, name, args...)
	c.Env = append(c.Environ(), "DEBIAN_FRONTEND=noninteractive")
	b, err := c.CombinedOutput()
	out := string(b)
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			Logf("%s: %s", name, l)
			if onLine != nil {
				onLine(l)
			}
		}
	}
	return out, err
}

// lastLines is the last n non-empty lines of s, joined with " / ".
func lastLines(s string, n int) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return strings.Join(out, " / ")
}
