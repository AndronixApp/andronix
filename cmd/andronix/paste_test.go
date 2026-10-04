package main

import (
	"strings"
	"testing"
)

// The app's one-line command pasted twice before Enter (apps 10.0, 10.0.1).
func TestUnglue(t *testing.T) {
	for _, c := range []struct {
		in, want string
		pasted   bool
	}{
		{"install debian --de xfceexport ANDRONIX_INSTALL_ID=0123456789abcdef", "install debian --de xfce", true},
		{"install ubuntu --de kdeexport ANDRONIX_INSTALL_ID=0123456789abcdef", "install ubuntu --de kde", true},
		{"install debian --de=lxqtexport", "install debian --de=lxqt", true},
		{"install debianexport ANDRONIX_INSTALL_ID=0123456789abcdef", "install debian", true},
		{"install debian --de xfce --yesexport ANDRONIX_INSTALL_ID=x", "install debian --de xfce --yes", true},
		{"remove ubuntu-xfce --legacyexport ANDRONIX_INSTALL_ID=0123456789abcdef", "remove ubuntu-xfce --legacy", true},
		// a space before export: the lone word goes too
		{"install debian --de xfce export ANDRONIX_INSTALL_ID=0123456789abcdef", "install debian --de xfce", true},
		// a Modded token: only stripped when the second id shows the double paste
		{"install --edition ubuntu-xfce --token k=K&e=1&h=abcexport ANDRONIX_INSTALL_ID=0123456789abcdef",
			"install --edition ubuntu-xfce --token k=K&e=1&h=abc", true},
		{"install --edition ubuntu-xfce --token k=K&e=1&h=abcexport", "install --edition ubuntu-xfce --token k=K&e=1&h=abcexport", false},
		// a typo glued to export (seen in telemetry): the typo stays for the
		// installer to forgive (xfc -> XFCE)
		{"install debian --de xfcexport", "install debian --de xfc", true},
		{"install debiaexport ANDRONIX_INSTALL_ID=0123456789abcdef", "install debia", true},
		// no paste: untouched
		{"install debian --de xfce --yes", "install debian --de xfce --yes", false},
		{"install debian --de nosuchexport", "install debian --de nosuchexport", false},
	} {
		got, pasted := unglue(strings.Fields(c.in))
		if strings.Join(got, " ") != c.want || pasted != c.pasted {
			t.Errorf("%q:\n got  %q %v\n want %q %v", c.in, strings.Join(got, " "), pasted, c.want, c.pasted)
		}
	}
}
