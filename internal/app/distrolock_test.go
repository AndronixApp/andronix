package app

import (
	"errors"
	"os"

	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Two installs of one distro at once: the second stops at once with
// install_running, naming what the first is doing; another distro isn't
// held up; the lock is free again after the first ends.
func TestDistroLockTwoInstalls(t *testing.T) {
	t.Setenv("ANDRONIX_HOME", t.TempDir())
	kali, _ := conf.LoadDistro("kali")
	debian, _ := conf.LoadDistro("debian")
	unlock, err := lockDistro(kali, "install")
	if err != nil {
		t.Fatal(err)
	}
	_, err = lockDistro(kali, "install")
	var e *ui.UserError
	if !errors.As(err, &e) || e.Class != "install_running" || !strings.Contains(e.Title, "is being installed in another Termux session") || !strings.Contains(e.What, "process") {
		t.Fatalf("second install: %v", err)
	}
	if _, err := lockDistro(kali, "update"); err == nil {
		t.Fatal("an update ran during the install")
	}
	u2, err := lockDistro(debian, "install")
	if err != nil {
		t.Fatalf("another distro held up: %v", err)
	}
	u2()
	unlock()
	u3, err := lockDistro(kali, "remove")
	if err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
	u3()
}

// A run that crashes (kill -9) never leaves the distro locked.
func TestDistroLockCrashedHolder(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANDRONIX_HOME", home)
	c := sys.Command(os.Args[0], "-test.run=^TestDistroLockHelper$")
	c.Env = append(os.Environ(), "ANDRONIX_LOCK_HELPER=1", "ANDRONIX_HOME="+home)
	out, _ := c.StdoutPipe()
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	b := make([]byte, 6)
	out.Read(b) // "locked"
	kali, _ := conf.LoadDistro("kali")
	if _, err := lockDistro(kali, "install"); err == nil {
		t.Fatal("got the lock while the helper held it")
	}
	c.Process.Signal(syscall.SIGKILL)
	c.Wait()
	unlock, err := lockDistro(kali, "install")
	if err != nil {
		t.Fatalf("a killed run still holds the lock: %v", err)
	}
	unlock()
}

// The helper process for TestDistroLockCrashedHolder: takes kali's lock
// and waits to be killed.
func TestDistroLockHelper(t *testing.T) {
	if os.Getenv("ANDRONIX_LOCK_HELPER") != "1" {
		t.Skip("helper")
	}
	kali, _ := conf.LoadDistro("kali")
	if _, err := lockDistro(kali, "install"); err != nil {
		os.Exit(2)
	}
	os.Stdout.WriteString("locked")
	time.Sleep(time.Minute)
}
