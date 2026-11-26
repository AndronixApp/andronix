package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// One install, update, remove or restore per distro at a time (support
// #313: a second 'andronix install kali' in another Termux session ran into
// the first one's apt lock and failed, twice, while the first finished
// fine). A flock on ~/.andronix/locks/<id>.lock, held by this process only
// (close-on-exec: proot and apt don't inherit it), so it goes away with
// the process however it ends: a crashed or killed run never blocks the
// next one. The file says which command holds it, for the message.
//
// So when this lock refuses a run, a live andronix holds it. Card #395
// ("install already running" after a Stop in the app) was such a run, not
// a stale lock: on Android 11 the app loses sight of the installer it
// started, and the install goes on unseen. See "Android 11" in
// docs/progress-json.md.

// lockDistro takes d's lock for verb ("install", "update", "remove",
// "restore"), or returns install_running when another andronix has it.
func lockDistro(d *conf.Distro, verb string) (unlock func(), err error) {
	dir := filepath.Join(GetPaths().Home, "locks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return func() {}, nil // no lock rather than no install
	}
	f, err := os.OpenFile(filepath.Join(dir, d.ID+".lock"), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return func() {}, nil
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		holder, _ := os.ReadFile(f.Name())
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, distroBusy(d, string(holder))
		}
		return func() {}, nil
	}
	f.Truncate(0)
	f.WriteAt([]byte(fmt.Sprintf("%s %d\n", verb, os.Getpid())), 0)
	return func() {
		syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		f.Close()
	}, nil
}

// distroLockHeld reports whether an install, update, remove or restore of
// the distro whose rootfs this is holds its lock right now (this process's
// own included). False when the lock can't be told: a rootfs outside
// ~/.andronix/distros (inside a distro, tests), no lock file yet. The probe
// is a shared lock taken and dropped at once; a lockDistro of another
// process in that instant is refused (install_running, naming the last
// holder). The one caller asks only with a killed run's lines in /etc/hosts,
// so that takes a killed Alpine run, then a login and an install started
// within the same microseconds.
func distroLockHeld(rootfs string) bool {
	dir := filepath.Dir(filepath.Clean(rootfs))
	p := GetPaths()
	if filepath.Base(filepath.Clean(rootfs)) != "rootfs" || filepath.Dir(dir) != filepath.Clean(p.Distros) {
		return false
	}
	f, err := os.Open(filepath.Join(p.Home, "locks", filepath.Base(dir)+".lock"))
	if err != nil {
		return false
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_SH|syscall.LOCK_NB); err != nil {
		return errors.Is(err, syscall.EWOULDBLOCK)
	}
	syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return false
}

// distroBusy is the error for a distro another andronix is working on;
// holder is its lock file ("install 1234").
func distroBusy(d *conf.Distro, holder string) error {
	verb, pid := "install", ""
	if f := strings.Fields(holder); len(f) == 2 {
		verb, pid = f[0], f[1]
	}
	doing := map[string]string{"install": "installed", "update": "updated", "remove": "removed", "restore": "restored"}[verb]
	if doing == "" {
		doing = "changed"
	}
	what := "Another andronix is working on " + d.Label() + " right now, and two at once would get in each other's way."
	if _, err := strconv.Atoi(pid); err == nil {
		what = "Another andronix (process " + pid + ") is working on " + d.Label() + " right now, and two at once would get in each other's way."
	}
	if InApp() {
		return &ui.UserError{Title: d.Label() + " is being " + doing + " in the Andronix app already", Class: "install_running",
			What: "Two at once would get in each other's way.", Fix: "Wait for it to finish, then try again."}
	}
	return &ui.UserError{Title: d.Label() + " is being " + doing + " in another Termux session", Class: "install_running",
		What: what, Fix: "Wait for it to finish, or close it, then run the same command again."}
}
