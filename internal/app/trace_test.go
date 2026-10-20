package app

import (
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestTraceOffPrintsNothing(t *testing.T) {
	t.Setenv("ANDRONIX_TRACE", "")
	out := filepath.Join(t.TempDir(), "procs.txt")
	traceProcs(os.Getpid(), out)()
	if _, err := os.Stat(out); err == nil {
		t.Fatal("the sampler wrote a file with tracing off")
	}
}

func TestTraceProcsSeesAChild(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("reads /proc")
	}
	t.Setenv("ANDRONIX_TRACE", "1")
	out := filepath.Join(t.TempDir(), "procs.txt")
	stop := traceProcs(os.Getpid(), out)
	c := sys.Command("sleep", "0.5")
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	c.Wait()
	time.Sleep(300 * time.Millisecond)
	stop()
	time.Sleep(150 * time.Millisecond)
	b, _ := os.ReadFile(out)
	if !strings.Contains(string(b), " + ") || !strings.Contains(string(b), "sleep 0.5") || !strings.Contains(string(b), " - ") {
		t.Fatalf("no start and end of the child in:\n%s", b)
	}
}

func TestArgv0(t *testing.T) {
	if argv0("/usr/bin/xfwm4 --compositor=off") != "/usr/bin/xfwm4" || argv0("Xvfb") != "Xvfb" {
		t.Fatal("argv0")
	}
}
