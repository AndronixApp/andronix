package app

import (
	"io"
	"os"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/termux"
)

// The missing-app box names the download, the file and the docs page
// (support #36 #98 #99).
func TestX11AppBox(t *testing.T) {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	x11AppBox("Then, in Termux: andronix desktop kali")
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	out := string(b)
	for _, want := range []string{termux.X11AppURL, termux.X11AppAPK, termux.X11Docs, "andronix desktop kali"} {
		if !strings.Contains(strings.Join(strings.Fields(out), " "), want) {
			t.Errorf("box lacks %q:\n%s", want, out)
		}
	}
	if os.Getenv("SHOW_BOX") != "" {
		t.Log("\n" + out)
	}
}
