package app

import (
	"os"
	"path/filepath"
	"strings"
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

// The session's log is emptied once it's over its cap; its appending
// writer goes on from the start.
func TestCapLog(t *testing.T) {
	defer func(d time.Duration) { capLogEvery = d }(capLogEvery)
	capLogEvery = 50 * time.Millisecond
	p := filepath.Join(t.TempDir(), "session.log")
	w, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	w.Write(make([]byte, 3<<20))
	go capLog(p, 1<<20)
	time.Sleep(300 * time.Millisecond)
	w.Write([]byte("after\n"))
	b, _ := os.ReadFile(p)
	if len(b) > 1<<10 || !strings.Contains(string(b), "was emptied") || !strings.HasSuffix(string(b), "after\n") {
		t.Errorf("log: %d bytes, %q", len(b), b[:min(len(b), 200)])
	}
	os.Remove(p) // ends capLog
}
