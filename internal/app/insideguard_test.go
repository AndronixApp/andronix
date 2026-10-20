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
	t.Setenv("ANDRONIX_HOST", "")
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

// In the app's distros the note points to the app, and the host kind is
// kept in the rootfs for andronix run through sudo (android's review).
func TestInsideDistroStandalone(t *testing.T) {
	t.Setenv("ANDRONIX_DISTRO", "debian")
	t.Setenv("ANDRONIX_HOST", "standalone")
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	ok := InsideDistro("install", []string{"install", "kali"})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	if !ok || !strings.Contains(string(out), "Andronix app") || strings.Contains(string(out), "Termux") {
		t.Errorf("standalone: %v\n%s", ok, out)
	}

	root := t.TempDir()
	t.Setenv("ANDRONIX_DISTRO", "")
	writeHostKind(root)
	if b, _ := os.ReadFile(root + "/" + hostFile); strings.TrimSpace(string(b)) != "standalone" {
		t.Errorf("host file: %q", b)
	}
	t.Setenv("ANDRONIX_HOST", "")
	writeHostKind(root)
	if b, _ := os.ReadFile(root + "/" + hostFile); strings.TrimSpace(string(b)) != "linux" {
		t.Errorf("host file after a Linux start: %q", b)
	}
}

// "andronix update" inside a distro of the app: no "do it from the app"
// (the app has no Update action), but where updates really come from.
func TestInsideDistroStandaloneUpdate(t *testing.T) {
	t.Setenv("ANDRONIX_HOST", "standalone")
	t.Setenv("ANDRONIX_DISTRO", "debian")
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	ok := InsideDistro("update", []string{"update"})
	w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	s := string(out)
	if !ok || !strings.Contains(s, "Google Play") || !strings.Contains(s, "apt update") || strings.Contains(s, "from the app's screens") || strings.Contains(s, "Type exit") {
		t.Errorf("update inside debian (app):\n%s", s)
	}
	for _, c := range []struct {
		distro string
		root   bool
		want   string
	}{
		{"debian", false, "sudo apt update && sudo apt full-upgrade"},
		{"debian", true, "  apt update && apt full-upgrade"},
		{"fedora", false, "sudo dnf upgrade"},
		{"arch", false, "sudo pacman -Syu"},
		{"alpine", false, "sudo apk upgrade -U"},
		{"void", true, "  xbps-install -Su"},
		{"manjaro", false, "  pacman -Syu"},
	} {
		got := strings.Join(standaloneUpdateLines(c.distro, c.root), "\n")
		if !strings.Contains(got, c.want) || !strings.Contains(got, "Google Play") {
			t.Errorf("%s root=%v:\n%s\nwant %q", c.distro, c.root, got, c.want)
		}
		if c.distro == "manjaro" && strings.Contains(got, "sudo pacman") {
			t.Errorf("manjaro has no sudo under proot:\n%s", got)
		}
	}
}

// 'andronix desktop' (and x11) inside a distro (support #351): the app's
// terminal says where desktops start; Termux keeps its two steps; vnc runs.
func TestInsideDistroDesktop(t *testing.T) {
	capture := func(cmd string, args []string) (bool, string) {
		r, w, _ := os.Pipe()
		old := os.Stdout
		os.Stdout = w
		ok := InsideDistro(cmd, args)
		w.Close()
		os.Stdout = old
		out, _ := io.ReadAll(r)
		return ok, string(out)
	}
	t.Setenv("ANDRONIX_DISTRO", "kali")
	t.Setenv("ANDRONIX_HOST", "standalone")
	for _, c := range [][]string{{"desktop", "kali"}, {"desktop", "stop"}, {"x11", "--legacy-drawing"}} {
		ok, out := capture(c[0], c)
		if !ok || !strings.Contains(out, "In the app, start desktops from Home > Your Linux.") || strings.Contains(out, "Termux") {
			t.Errorf("app, %v: %v\n%s", c, ok, out)
		}
	}
	t.Setenv("ANDRONIX_HOST", "termux")
	ok, out := capture("desktop", []string{"desktop", "kali"})
	if !ok || !strings.Contains(out, "The desktop opens from Termux") || !strings.Contains(out, "andronix desktop kali") || !strings.Contains(out, "Type exit") {
		t.Errorf("Termux: %v\n%s", ok, out)
	}
	if ok, _ := capture("vnc", []string{"vnc", "start"}); ok {
		t.Error("vnc inside a distro: want it to run")
	}
}
