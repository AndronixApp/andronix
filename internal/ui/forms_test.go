package ui

import (
	"strings"
	"testing"
)

func TestPasswordDesc(t *testing.T) {
	old := ASCII
	defer func() { ASCII = old }()
	ASCII = false
	if got, want := passwordDesc("Used for sudo."), "Used for sudo. Each character shows as *."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	ASCII = true
	if got, want := passwordDesc(""), "Nothing shows while you type; that's normal."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// In the Andronix app "This needs a terminal" points to the app's terminal,
// not to Termux (owner, 10.1); the command line keeps its text.
func TestNoTerminalFixInApp(t *testing.T) {
	t.Setenv("ANDRONIX_HOST", "standalone")
	if f := noTerminal().Fix; strings.Contains(f, "Termux") || !strings.Contains(f, "Andronix app") {
		t.Errorf("app: %q", f)
	}
	t.Setenv("ANDRONIX_HOST", "")
	if f := noTerminal().Fix; !strings.Contains(f, "Termux's own window") {
		t.Errorf("Termux: %q", f)
	}
}
