package termux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// After a mirror fails, the next one not tried yet; x11 follows main; the
// frozen Android 5/6 repositories are left alone.
func TestNextMirror(t *testing.T) {
	pre := filepath.Join(t.TempDir(), "com.termux/files/usr")
	os.MkdirAll(filepath.Join(pre, "etc/apt/sources.list.d"), 0o755)
	t.Setenv("PREFIX", pre)
	mirrorsTried = map[string]bool{}
	list, x11 := filepath.Join(pre, "etc/apt/sources.list"), filepath.Join(pre, "etc/apt/sources.list.d/x11.list")
	os.WriteFile(list, []byte("deb https://mirrors.ustc.edu.cn/termux/termux-main stable main\n"), 0o644)
	os.WriteFile(x11, []byte("deb https://packages-cf.termux.dev/apt/termux-x11/ x11 main\n"), 0o644)
	if !nextMirror() {
		t.Fatal("no switch")
	}
	b, _ := os.ReadFile(list)
	c, _ := os.ReadFile(x11)
	if !strings.Contains(string(b), "deb "+termuxMirrors[0][0]+" stable main") || string(c) != "deb "+termuxMirrors[0][1]+" x11 main\n" {
		t.Errorf("after one switch:\n%s%s", b, c)
	}
	for i := 1; i < len(termuxMirrors); i++ {
		if !nextMirror() {
			t.Fatalf("switch %d", i)
		}
	}
	if nextMirror() {
		t.Error("switched past the last mirror")
	}
	mirrorsTried = map[string]bool{}
	os.WriteFile(list, []byte("deb https://termux.net/apt/termux-packages-24 stable main\n"), 0o644)
	if nextMirror() {
		t.Error("switched the Android 5/6 repository")
	}
}

func TestX11Args(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANDRONIX_HOME", "")
	os.MkdirAll(filepath.Dir(filepath.Dir(logPath())), 0o755)
	if a := X11SavedArgs(); len(a) != 0 {
		t.Fatalf("saved before: %v", a)
	}
	SaveX11Args([]string{"-legacy-drawing", "-force-bgra"})
	if a := X11SavedArgs(); strings.Join(a, " ") != "-legacy-drawing -force-bgra" || X11ArgsLabel(a) != "both" {
		t.Errorf("saved: %v", a)
	}
	os.WriteFile(x11ArgsFile(), []byte("-legacy-drawing -nolisten; rm -rf /\n"), 0o644)
	if a := X11SavedArgs(); strings.Join(a, " ") != "-legacy-drawing" {
		t.Errorf("unknown options kept: %v", a)
	}
	SaveX11Args(nil)
	if a := X11SavedArgs(); len(a) != 0 || X11ArgsLabel(a) != "none" {
		t.Errorf("after clearing: %v", a)
	}
}
