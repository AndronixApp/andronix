package rootfs

import (
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Ctrl+C at the first-boot user setup (welcome exits 10, or 130 while
// firstboot is pending) ends the login shell instead of leaving root
// without a user; otherwise the shell goes on.
func TestProfileWelcomeStopped(t *testing.T) {
	for _, c := range []struct {
		code      string
		firstboot bool
		ends      bool
	}{{"10", false, true}, {"130", true, true}, {"130", false, false}, {"0", true, false}, {"1", false, false}} {
		d := t.TempDir()
		fake := filepath.Join(d, "andronix")
		os.WriteFile(fake, []byte("#!/bin/sh\nexit "+c.code+"\n"), 0o755)
		fb := filepath.Join(d, "firstboot")
		if c.firstboot {
			os.WriteFile(fb, nil, 0o644)
		}
		p := strings.NewReplacer("/usr/local/bin/andronix", fake, "/etc/andronix/firstboot", fb).Replace(profileSh)
		prof := filepath.Join(d, "profile.sh")
		os.WriteFile(prof, []byte(p), 0o644)
		out, _ := sys.Command("sh", "-ic", ". "+prof+"; echo SHELL-WENT-ON").CombinedOutput()
		if ends := !strings.Contains(string(out), "SHELL-WENT-ON"); ends != c.ends {
			t.Errorf("welcome exit %s, firstboot %v: ends=%v, want %v\n%s", c.code, c.firstboot, ends, c.ends, out)
		}
	}
}
