package app

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/compat"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Play Store Termux on a 32-bit phone gets the F-Droid advice (owner, #109);
// every other proot_exec failure asks for a report.
func TestProotExecError(t *testing.T) {
	const detail = `execve("/usr/bin/env"): No such file or directory`
	for _, c := range []struct {
		termux, arch string
		fdroid       bool
	}{
		{"google_play_store", "arm", true},
		{"google_play_store", "i686", true},
		{"google_play_store", "aarch64", false},
		{"github", "arm", false},
		{"f_droid", "arm", false},
		{"", "arm", false},
	} {
		e := prootExecError(detail, "Debian", c.termux, sys.Arch(c.arch))
		if e.Title != "Linux can't start on this phone" || e.Class != "proot_exec" || e.Kind != "proot_exec" || e.Detail != detail {
			t.Errorf("%s/%s: %+v", c.termux, c.arch, e)
		}
		got := strings.Contains(e.Fix, "f-droid.org/packages/com.termux")
		if got != c.fdroid {
			t.Errorf("%s/%s: F-Droid advice %v, want %v (%q)", c.termux, c.arch, got, c.fdroid, e.Fix)
		}
		if c.fdroid {
			if !strings.Contains(e.Fix, "Back up first") || !strings.Contains(e.Fix, "run the same command again") || !strings.Contains(e.What, "inside Debian.") {
				t.Errorf("%s/%s: %q / %q", c.termux, c.arch, e.What, e.Fix)
			}
		} else if !strings.Contains(e.Fix, "andronix report") || !strings.Contains(e.What, detail) {
			t.Errorf("%s/%s: %q / %q", c.termux, c.arch, e.What, e.Fix)
		}
	}
}

// Play Store Termux with a broken applib link (support #358): the exact fix,
// both before the install starts and when proot fails; a good link changes
// nothing.
func TestAppLibError(t *testing.T) {
	for _, k := range []string{"TERMUX_APP__APK_RELEASE", "TERMUX_APK_RELEASE", "TERMUX_APP__APP_VERSION_NAME", "ANDRONIX_HOST", "ANDRONIX_DISTRO"} {
		t.Setenv(k, "")
	}
	t.Setenv("TERMUX_VERSION", "googleplay.2026.10.09")
	root := t.TempDir()
	files := filepath.Join(root, "data", "com.termux", "files")
	os.MkdirAll(filepath.Join(files, "usr"), 0o755)
	t.Setenv("PREFIX", filepath.Join(files, "usr"))
	apk := filepath.Join(root, "app", "lib", "arm64")
	os.MkdirAll(apk, 0o755)
	os.WriteFile(filepath.Join(apk, "libproot-loader.so"), []byte("x"), 0o755)
	applib := filepath.Join(files, "applib")
	const detail = `execve("/usr/bin/env"): Permission denied`

	os.MkdirAll(applib, 0o755) // restored as a folder
	err := appLibCheck()
	e, ok := err.(*ui.UserError)
	if !ok || e.Class != "termux_applib" || !strings.Contains(e.What, "a folder instead of a link") ||
		!strings.Contains(e.Fix, "rm -rf ~/../applib") || !strings.Contains(e.Fix, "Force stop") || !strings.Contains(e.Fix, "erases everything inside Termux") {
		t.Fatalf("folder: %#v", err)
	}
	e = prootExecError(detail, "Arch Linux", "google_play_store", "aarch64")
	if e.Class != "termux_applib" || e.Kind != "proot_exec" || e.Detail != detail {
		t.Errorf("proot failure with a folder: %+v", e)
	}

	os.RemoveAll(applib)
	os.Symlink(apk, applib)
	if err := appLibCheck(); err != nil {
		t.Errorf("good link: %v", err)
	}
	if e := prootExecError(detail, "Arch Linux", "google_play_store", "aarch64"); e.Class != "proot_exec" || !strings.Contains(e.Fix, "andronix report") {
		t.Errorf("good link, proot failure: %+v", e)
	}

	t.Setenv("TERMUX_VERSION", "0.118.3") // F-Droid/GitHub: no applib, never checked
	os.Remove(applib)
	if err := appLibCheck(); err != nil {
		t.Errorf("F-Droid Termux: %v", err)
	}
}

// Support #5402: the prober itself crashed inside the distro (Go's runtime
// on a 32-bit phone: "futexwakeup ... returned -38", SIGSEGV, exit status
// 2). A probe that measures nothing must not stop setup, whatever it
// printed; only proot failing to start any program (execve) does.
func TestProbeFailedStopsOnlyOnProotExec(t *testing.T) {
	c := &compatRun{name: "Kali Linux", facts: compat.Facts{Probe: &compat.Probe{}}}
	crash := strings.Join([]string{
		"futexwakeup addr=0xc804e324 returned -38",
		"SIGSEGV: segmentation violation",
		"PC=0x64488 m=0 sigcode=1 addr=0x1006",
		"",
		"goroutine 0 gp=0xa9a468 m=0 mp=0xa9bcf0 [idle]:",
		"runtime.futexwakeup(0xc804e324, 0x1)",
		"    runtime/os_linux.go:98 +0x70 fp=0xff9ac2e4 sp=0xff9ac2b8 pc=0x64488",
		"runtime.morestack()",
		"    runtime/asm_arm.s:406 +0x60 fp=0xff9ac4f8 sp=0xff9ac4f4 pc=0xac23c",
		"trap    0x0",
		"fault   0x1006",
	}, "\n")
	for name, out := range map[string]string{
		"go runtime crash": crash,
		"morestack on g0":  "fatal: morestack on g0\n",
		"killed":           "",
		"sh error":         "/bin/sh: 1: /usr/local/bin/andronix: Exec format error\n",
	} {
		for _, err := range []error{errors.New("exit status 2"), errors.New("signal: killed"), nil} {
			if got := c.probeFailed(out, err); got != nil {
				t.Errorf("%s (%v): the probe stopped the install: %v", name, err, got)
			}
		}
	}
	out := "proot error: execve(\"/usr/bin/env\"): No such file or directory\nfatal error: see `proot --help`.\n"
	var ue *ui.UserError
	if err := c.probeFailed(out, errors.New("exit status 1")); !errors.As(err, &ue) || ue.Class != "proot_exec" {
		t.Errorf("proot execve failure: got %v, want the proot_exec error", err)
	}
}
