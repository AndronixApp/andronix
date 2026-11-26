package sys

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Card #395: a cancelled context stops the child by pid, and Wait says so.
func TestCommandContextCancelKillsByPid(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Before it starts there is no process: sys.Kill says done, where
	// exec's own Cancel (Process.Kill) would panic on the nil Process. This
	// is what tells the two apart without an Android 11 kernel.
	if c := CommandContext(ctx, "sleep", "30"); c.Cancel == nil {
		t.Fatal("CommandContext set no Cancel")
	} else if err := c.Cancel(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Cancel before Start = %v, want os.ErrProcessDone (sys.Kill)", err)
	}
	c := CommandContext(ctx, "sleep", "30")
	if err := c.Start(); err != nil {
		t.Skipf("no sleep to run: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	cancel()
	select {
	case err := <-done:
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("Wait after cancel: %v, want an ExitError", err)
		}
		if ws, ok := ee.Sys().(syscall.WaitStatus); !ok || !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
			t.Fatalf("the child ended with %v, want SIGKILL", ee)
		}
	case <-time.After(10 * time.Second):
		c.Process.Kill()
		t.Fatal("the child was still running 10 s after the cancel")
	}
}

// Without a context there is nothing to cancel: exec's default stays.
func TestCommandHasNoCancel(t *testing.T) {
	if c := Command("sleep", "0"); c.Cancel != nil {
		t.Fatal("Command set a Cancel without a context")
	}
}

func TestSignalAndKill(t *testing.T) {
	if err := Kill(nil); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill(nil) = %v", err)
	}
	if err := SignalPid(0, syscall.SIGTERM); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("SignalPid(0) = %v: pid 0 is the caller's own group, never signalled", err)
	}
	if err := SignalPid(-1, syscall.SIGTERM); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("SignalPid(-1) = %v: -1 is every process, never signalled", err)
	}
	c := Command("sleep", "30")
	if err := c.Start(); err != nil {
		t.Skipf("no sleep to run: %v", err)
	}
	if err := Signal(c.Process, syscall.SIGTERM); err != nil {
		t.Fatalf("Signal: %v", err)
	}
	err := c.Wait()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
		t.Fatalf("the child ended with %v, want SIGTERM", err)
	}
	// Reaped: its pid is gone (ESRCH), which reads as done.
	if err := Kill(c.Process); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("Kill after Wait = %v, want os.ErrProcessDone", err)
	}
}

// Nothing signals through os.Process (or looks a process up to signal
// it): on Android 11 that is a SIGSYS. New code has to use Signal, Kill
// or SignalPid. Cmd.WaitDelay is out too: with it, os/exec calls
// Process.Kill itself, past Cmd.Cancel. This reads the source for those
// exact spellings; a Process kept in another variable gets past it. A
// WaitDelay line with a "// pidfd-ok: <why>" comment is let through: for
// a use that can be shown not to reach the system call.
func TestNoProcessSignalOutsideSys(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	// \b, not "(": a method value (c.Cancel = c.Process.Kill) counts too.
	bad := regexp.MustCompile(`\.Process\.(Kill|Signal)\b|os\.FindProcess\(|\.WaitDelay\b`)
	signals := regexp.MustCompile(`\.Process\.(Kill|Signal)\b|os\.FindProcess\(`)
	files := 0
	var allowed []string
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			files++
			for i, line := range strings.Split(string(b), "\n") {
				code := strings.TrimSpace(line)
				if strings.HasPrefix(code, "//") || !bad.MatchString(code) {
					continue
				}
				rel, _ := filepath.Rel(root, p)
				// Only a WaitDelay can be let through, never a signal itself.
				if _, why, ok := strings.Cut(code, "// pidfd-ok:"); ok && !signals.MatchString(code) {
					if strings.TrimSpace(why) == "" {
						t.Errorf("%s:%d: pidfd-ok without a reason", rel, i+1)
					}
					allowed = append(allowed, rel)
					continue
				}
				t.Errorf("%s:%d: %s: use sys.Signal, sys.Kill or sys.SignalPid (and no WaitDelay)", rel, i+1, code)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
	}
	if files < 50 {
		t.Fatalf("only %d source files read: the check didn't see the tree", files)
	}
	// The uses let through are listed here, so a new one is a decision.
	if want := []string{filepath.Join("internal", "proot", "proot.go")}; !slices.Equal(allowed, want) {
		t.Errorf("pidfd-ok lines in %v, want only %v", allowed, want)
	}
}
