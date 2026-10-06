package app

import (
	"io"
	"os"
	"strings"
	"testing"
)

// Support #49 #93: Termux-only commands typed inside a distro get the
// two-step note instead of failing on Termux checks; the rest run.
func TestInsideDistro(t *testing.T) {
	t.Setenv("ANDRONIX_DISTRO", "")
	if InsideDistro("install", []string{"install", "debian"}) && !inGuest() {
		t.Error("on the host: want no note")
	}
	t.Setenv("ANDRONIX_DISTRO", "debian")
	if InsideDistro("display", []string{"display", "start", "--max-size", "1600x1600"}) {
		t.Error("display start (the standalone display's guest step) must run")
	}
	for _, c := range []string{"pack", "vnc", "report", "version", "telemetry", "setup-user", "session-prep"} {
		if InsideDistro(c, []string{c}) {
			t.Errorf("%s inside a distro: want it to run", c)
		}
	}
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	ok := InsideDistro("install", []string{"install", "ubuntu", "--de", "xfce"})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if !ok || !strings.Contains(string(out), "andronix install ubuntu --de xfce") || !strings.Contains(string(out), "Type exit") {
		t.Errorf("install inside debian: %v\n%s", ok, out)
	}
}
