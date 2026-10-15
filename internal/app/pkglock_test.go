package app

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/charmbracelet/log"
)

type lineRep struct{ lines []string }

func (r *lineRep) Progress(int64, int64, string) {}
func (r *lineRep) Line(s string)                 { r.lines = append(r.lines, s) }
func (r *lineRep) Detail(string)                 {}
func (r *lineRep) Label(string)                  {}

// fakeProc writes /proc/<pid>/stat and cmdline under a temp procRoot.
type fakeProc struct{ root string }

func (f fakeProc) add(pid, ppid int, comm, start string, args ...string) {
	d := filepath.Join(f.root, strconv.Itoa(pid))
	os.MkdirAll(d, 0o755)
	// pid (comm) state ppid, then 16 fields, the 20th after ")" being the start time
	fields := []string{"S", strconv.Itoa(ppid)}
	for len(fields) < 19 {
		fields = append(fields, "0")
	}
	fields = append(fields, start, "0")
	os.WriteFile(filepath.Join(d, "stat"), []byte(fmt.Sprintf("%d (%s) %s\n", pid, comm, strings.Join(fields, " "))), 0o644)
	if len(args) == 0 {
		args = []string{comm}
	}
	os.WriteFile(filepath.Join(d, "cmdline"), []byte(strings.Join(args, "\x00")+"\x00"), 0o644)
}

func (f fakeProc) gone(pid int) { os.RemoveAll(filepath.Join(f.root, strconv.Itoa(pid))) }

// A distro's proot (pid 100) running sh (101) running apt-get (102).
func lockSetup(t *testing.T) (fakeProc, *pkgOps, *bytes.Buffer, *[]int) {
	f := fakeProc{t.TempDir()}
	old := []any{procRoot, lockWait, lockPoll, signalProc}
	t.Cleanup(func() {
		procRoot, lockWait, lockPoll = old[0].(string), old[1].(time.Duration), old[2].(time.Duration)
		signalProc = old[3].(func(int, syscall.Signal) error)
	})
	procRoot, lockWait, lockPoll = f.root, 10*time.Second, time.Second
	f.add(1, 0, "init", "1")
	f.add(50, 1, "andronix", "40")
	// orphaned: its andronix is gone, the proot belongs to init now
	f.add(100, 1, "proot", "500", "/data/x/proot", "--kill-on-exit", "-r", "/r/debian/rootfs", "/bin/sh", "-c", "apt-get update")
	f.add(101, 100, "sh", "501")
	f.add(102, 101, "apt-get", "502")
	var signalled []int
	signalProc = func(pid int, _ syscall.Signal) error {
		signalled = append(signalled, pid)
		f.gone(102)
		f.gone(101)
		f.gone(pid)
		return nil
	}
	var buf bytes.Buffer
	p := &pkgOps{t: &proot.Target{Rootfs: "/r/debian/rootfs"}, lg: &Logger{Logger: log.New(&buf)}, sleep: func(time.Duration) {}}
	return f, p, &buf, &signalled
}

const heldLine = "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 102 (apt-get)"

func TestLockHolderFinishes(t *testing.T) {
	f, p, buf, sig := lockSetup(t)
	n := 0
	p.sleep = func(time.Duration) {
		if n++; n == 3 {
			f.gone(102)
		}
	}
	if !p.waitOrStopHolder(context.Background(), heldLine, &lineRep{}) || len(*sig) != 0 {
		t.Fatalf("finished holder: signalled %v\n%s", *sig, buf)
	}
	if !strings.Contains(buf.String(), "lock held by an earlier run (apt-get, pid 102, proot 100)") {
		t.Errorf("log: %s", buf)
	}
}

func TestLockHolderStopped(t *testing.T) {
	_, p, buf, sig := lockSetup(t)
	if !p.waitOrStopHolder(context.Background(), heldLine, &lineRep{}) {
		t.Fatalf("not retried\n%s", buf)
	}
	if len(*sig) != 1 || (*sig)[0] != 100 {
		t.Fatalf("signalled %v, want the proot (100)", *sig)
	}
	if !strings.Contains(buf.String(), "stopping its proot 100") {
		t.Errorf("log: %s", buf)
	}
}

// The pid apt named was reused by something else while waiting: never signalled.
func TestLockHolderPidReused(t *testing.T) {
	f, p, buf, sig := lockSetup(t)
	n := 0
	p.sleep = func(time.Duration) {
		if n++; n == 2 {
			f.gone(102)
			f.add(102, 101, "apt-get", "999") // same pid and parent, a new process
		}
	}
	p.waitOrStopHolder(context.Background(), heldLine, &lineRep{})
	if len(*sig) != 0 {
		t.Fatalf("signalled %v after the pid was reused\n%s", *sig, buf)
	}
}

// A holder under another distro's proot, or outside any proot: left alone.
func TestLockHolderNotOurs(t *testing.T) {
	f, p, _, sig := lockSetup(t)
	f.add(100, 1, "proot", "500", "/data/x/proot", "-r", "/r/ubuntu/rootfs", "/bin/sh")
	if p.waitOrStopHolder(context.Background(), heldLine, &lineRep{}) || len(*sig) != 0 {
		t.Fatalf("another distro's holder: signalled %v", *sig)
	}
	f.add(102, 1, "apt-get", "502")
	if findLockHolder("/r/debian/rootfs", heldLine) != nil {
		t.Fatal("a holder outside any proot counted as ours")
	}
}

// pacman: no pid in its message, found by name under our proot.
func TestLockHolderScan(t *testing.T) {
	f, _, _, _ := lockSetup(t)
	f.gone(102)
	f.add(103, 101, "pacman", "503")
	h := findLockHolder("/r/debian/rootfs", "error: failed to init transaction (unable to lock database)")
	if h == nil || h.pid != 103 || h.proot != 100 {
		t.Fatalf("scan: %+v", h)
	}
	f.gone(103)
	if findLockHolder("/r/debian/rootfs", "") != nil {
		t.Fatal("no pacman left, still a holder")
	}
}

// support #313: the holder's proot belongs to an andronix that is still
// running (a second install in another Termux session): never waited for
// or stopped, and reported as another andronix at work.
func TestLockHolderLiveAndronix(t *testing.T) {
	f, p, buf, sig := lockSetup(t)
	f.add(50, 1, "andronix", "40", "/data/data/com.termux/files/usr/bin/andronix", "install", "debian")
	f.add(100, 50, "proot", "500", "/data/x/proot", "--kill-on-exit", "-r", "/r/debian/rootfs", "/bin/sh", "-c", "apt-get install -y lxqt")
	slept := 0
	p.sleep = func(time.Duration) { slept++ }
	if p.waitOrStopHolder(context.Background(), heldLine, &lineRep{}) || len(*sig) != 0 || slept != 0 {
		t.Fatalf("live andronix's holder: retried, signalled %v, slept %d\n%s", *sig, slept, buf)
	}
	if p.liveHolder != 50 {
		t.Errorf("liveHolder %d, want 50", p.liveHolder)
	}
	// started through the linker: andronix is argv[1]
	f.add(50, 1, "linker64", "40", "/system/bin/linker64", "/data/data/com.termux/files/usr/bin/andronix", "install", "debian")
	if h := findLockHolder("/r/debian/rootfs", heldLine); h == nil || h.andronix != 50 {
		t.Fatalf("linker-started andronix not seen: %+v", h)
	}
}
