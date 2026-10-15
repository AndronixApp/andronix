package app

import (
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"strings"
	"testing"
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
