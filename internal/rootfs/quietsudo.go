package rootfs

import (
	"os"
	"path/filepath"
	"strings"
)

// quietSudoFile turns off sudo's Linux audit messages. Under proot every
// sudo printed "sudo: unable to send audit message: Operation not
// permitted" (Ubuntu 26.04's sudo.ws) even when it worked, and a user took
// that for sudo failing (support #253). log_allowed and log_denied gate the
// audit calls (and syslog, which proot has none of).
const quietSudoFile = "etc/sudoers.d/andronix-quiet"

const quietSudo = "# Written by the Andronix installer: no audit messages under proot.\nDefaults !log_allowed, !log_denied\n"

// QuietSudo writes quietSudoFile when the distro's sudo is the classic
// one. sudo-rs refuses settings it doesn't know (log_allowed), so with
// sudo-rs, or no sudo, or no sudoers.d, the file is removed instead.
func QuietSudo(root string) {
	p := filepath.Join(root, quietSudoFile)
	_, dirErr := os.Stat(filepath.Dir(p))
	if dirErr != nil || !classicSudo(root) {
		os.Remove(p)
		return
	}
	if b, err := os.ReadFile(p); err != nil || string(b) != quietSudo {
		writeFile(p, quietSudo, 0o440)
	}
}

// classicSudo: /usr/bin/sudo, followed through its links inside root
// (Ubuntu's alternatives: sudo -> /etc/alternatives/sudo -> sudo.ws), is
// a sudo that isn't sudo-rs.
func classicSudo(root string) bool {
	p := "/usr/bin/sudo"
	for i := 0; i < 10; i++ {
		t, err := os.Readlink(filepath.Join(root, p))
		if err != nil {
			break
		}
		if !filepath.IsAbs(t) {
			t = filepath.Join(filepath.Dir(p), t)
		}
		p = t
	}
	if strings.Contains(p, "sudo-rs") || strings.Contains(p, "/cargo/") {
		return false
	}
	st, err := os.Stat(filepath.Join(root, p))
	return err == nil && st.Mode().IsRegular()
}
