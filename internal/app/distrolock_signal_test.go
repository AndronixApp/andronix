package app

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Card #395 ("install already running" after a Stop): the distro lock is
// held by the running process itself and ends with it, whatever ends it.
// It is never left behind by a run that was cancelled or killed, and it
// stays for as long as a run is really going, also one the app lost.

// The helper process for the tests below, by ANDRONIX_LOCK_SIGNAL_HELPER:
//
//	install   an install as the lock sees it: SIGTERM and Ctrl-C cancel it
//	          (as in main), it takes kali's lock, says "locked" and waits.
//	setsid11  Android 11's setsid (toybox 0.8.3) in front of that install:
//	          it makes itself a group leader first, so its setsid() fails
//	          and it forks; the child runs the install in a session of its
//	          own and the parent exits 0 at once.
func TestDistroLockSignalHelper(t *testing.T) {
	switch os.Getenv("ANDRONIX_LOCK_SIGNAL_HELPER") {
	case "install":
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		kali, _ := conf.LoadDistro("kali")
		unlock, err := lockDistro(kali, "install")
		if err != nil {
			os.Exit(2)
		}
		os.Stdout.WriteString("locked\n")
		select {
		case <-ctx.Done():
		case <-time.After(time.Minute):
		}
		unlock()
		os.Exit(130)
	case "setsid11":
		syscall.Setpgid(0, 0)
		c := sys.Command(os.Args[0], "-test.run=^TestDistroLockSignalHelper$")
		c.Env = append(os.Environ(), "ANDRONIX_LOCK_SIGNAL_HELPER=install")
		c.Stdout, c.Stderr = os.Stdout, os.Stderr
		c.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := c.Start(); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	default:
		t.Skip("helper")
	}
}

// startLockHelper starts the helper as the app starts an install (it keeps
// the pid of the process it started) and waits until kali's lock is held.
// It returns that pid, the pid in the lock file, and a channel closed when
// the started process has ended.
func startLockHelper(t *testing.T, helper string) (started, holder int, ended chan struct{}) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ANDRONIX_HOME", home)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	c := sys.Command(os.Args[0], "-test.run=^TestDistroLockSignalHelper$")
	c.Env = append(os.Environ(), "ANDRONIX_LOCK_SIGNAL_HELPER="+helper, "ANDRONIX_HOME="+home)
	c.Stdout = w // a file, so Wait doesn't wait for a forked install
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	w.Close()
	ended = make(chan struct{})
	go func() { c.Wait(); close(ended) }()
	holderPid := func() int {
		b, _ := os.ReadFile(filepath.Join(home, "locks", "kali.lock"))
		if f := strings.Fields(string(b)); len(f) == 2 {
			pid, _ := strconv.Atoi(f[1])
			return pid
		}
		return 0
	}
	// Whatever the test does, what it started doesn't outlive it. A held
	// lock means its holder (the pid in the file) is still running.
	t.Cleanup(func() {
		if pid := holderPid(); pid > 1 && pid != os.Getpid() && !kaliLockFreeWithin(0) {
			syscall.Kill(pid, syscall.SIGKILL)
		}
		c.Process.Kill() // a no-op once it was waited for
		<-ended
	})
	r.SetReadDeadline(time.Now().Add(20 * time.Second))
	if line, err := bufio.NewReader(r).ReadString('\n'); err != nil || line != "locked\n" {
		t.Fatalf("the install didn't take the lock: %q, %v", line, err)
	}
	started, holder = c.Process.Pid, holderPid()
	if started <= 1 || holder <= 1 || holder == os.Getpid() {
		t.Fatalf("started %d, lock holder %d", started, holder)
	}
	return started, holder, ended
}

// kaliLockFreeWithin reports whether kali's lock can be taken within d.
func kaliLockFreeWithin(d time.Duration) bool {
	kali, _ := conf.LoadDistro("kali")
	for end := time.Now().Add(d); ; time.Sleep(20 * time.Millisecond) {
		if unlock, err := lockDistro(kali, "install"); err == nil {
			unlock()
			return true
		}
		if time.Now().After(end) {
			return false
		}
	}
}

func wantKaliRefused(t *testing.T, when string) {
	t.Helper()
	kali, _ := conf.LoadDistro("kali")
	unlock, err := lockDistro(kali, "install")
	var e *ui.UserError
	if !errors.As(err, &e) || e.Class != "install_running" {
		if err == nil {
			unlock()
		}
		t.Fatalf("a second install %s: %v, want install_running", when, err)
	}
}

// A cancelled install (SIGTERM: the app's Stop; Ctrl-C) never keeps the
// lock: the next install of that distro starts. (A killed one:
// TestDistroLockCrashedHolder.) The helper cancels as main does; the real
// binary's cancel was run by hand for card #395.
func TestDistroLockFreeAfterSignal(t *testing.T) {
	for _, sig := range []syscall.Signal{syscall.SIGTERM, syscall.SIGINT} {
		t.Run(sig.String(), func(t *testing.T) {
			started, holder, _ := startLockHelper(t, "install")
			if started != holder {
				t.Fatalf("started %d, lock holder %d: want one process", started, holder)
			}
			wantKaliRefused(t, "during the first")
			syscall.Kill(holder, sig)
			if !kaliLockFreeWithin(5 * time.Second) {
				t.Fatalf("the lock is still held after %v", sig)
			}
		})
	}
}

// What card #395 saw, as the lock sees it. On Android 11 the app (10.1.8
// to 10.2.3) starts the installer through a setsid that forks: the process
// the app started exits at once, and the install runs on as another
// process the app no longer knows (the pid it kept is gone, so its Stop
// has nothing to signal). A new install of that distro is refused,
// rightly: one is running. The lock is free the moment that install ends.
func TestDistroLockHeldByRunTheAppLost(t *testing.T) {
	started, holder, ended := startLockHelper(t, "setsid11")
	if holder == started {
		t.Fatalf("lock holder %d is the started process: want a forked install", holder)
	}
	select {
	case <-ended: // the app sees its process end (exit 0) with the install going
	case <-time.After(20 * time.Second):
		t.Fatal("the forking setsid didn't exit")
	}
	wantKaliRefused(t, "while the lost one runs")
	syscall.Kill(holder, syscall.SIGTERM)
	if !kaliLockFreeWithin(5 * time.Second) {
		t.Fatal("the lock is still held after the install ended")
	}
}
