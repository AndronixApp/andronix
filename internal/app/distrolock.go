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
	return &ui.UserError{Title: d.Label() + " is being " + doing + " in another Termux session", Class: "install_running",
		What: what, Fix: "Wait for it to finish, or close it, then run the same command again."}
}
