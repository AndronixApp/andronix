package app

import (
	"os"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// With --progress json, the run's log gets each event as progress: {json}.
func TestRunLogHasEvents(t *testing.T) {
	t.Setenv("ANDRONIX_HOME", t.TempDir())
	old := ui.JSON
	ui.JSON = true
	defer func() { ui.JSON = old; ui.SetEventLog(nil) }()
	lg := NewLog("install-test")
	ui.Emit("step", map[string]any{"n": 1, "key": "download"})
	lg.Close()
	b, _ := os.ReadFile(lg.Path)
	if !strings.Contains(string(b), `progress: {"key":"download","n":1,"t":"step"}`) {
		t.Errorf("run log:\n%s", b)
	}
}
