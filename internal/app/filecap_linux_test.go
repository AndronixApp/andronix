package app

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// A capped process is stopped (SIGXFSZ) at the cap, and the file stays
// at the cap: what a runaway X server's log gets.
func TestCapFileSize(t *testing.T) {
	f := filepath.Join(t.TempDir(), "log")
	c := sys.Command("sh", "-c", "sleep 0.5; while :; do echo WaitForSomething: poll: Function not implemented; done >"+f)
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	if err := capFileSize(c.Process.Pid, 64<<10); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		ws, _ := c.ProcessState.Sys().(syscall.WaitStatus)
		if !ws.Signaled() || ws.Signal() != syscall.SIGXFSZ {
			t.Errorf("ended with %v, want SIGXFSZ", err)
		}
	case <-time.After(20 * time.Second):
		c.Process.Kill()
		t.Fatal("still writing after 20 s")
	}
	if st, err := os.Stat(f); err != nil || st.Size() > 64<<10 {
		t.Errorf("log: %v %v", st.Size(), err)
	}
}
