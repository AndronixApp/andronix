package main

import (
	"strings"
	"testing"
)

// The app's Reinstall button (10.0.2) re-runs its command with --reinstall
// appended: after --de for free installs, after the quoted --token for
// Modded ones (the shell has already removed the quotes).
func TestReinstallAppended(t *testing.T) {
	for _, c := range []struct {
		cmd                   string
		distro, de, ed, token string
	}{
		{"install debian --de xfce --reinstall", "debian", "xfce", "", ""},
		{"install ubuntu-xfce --token k=KEY&e=1790000000&h=ab%2Bc --reinstall", "ubuntu-xfce", "", "", "k=KEY&e=1790000000&h=ab%2Bc"},
		{"install --edition kali-xfce --token k=KEY&e=1790000000&h=abc --reinstall", "", "", "kali-xfce", "k=KEY&e=1790000000&h=abc"},
	} {
		in, _ := unglue(strings.Fields(c.cmd))
		a := parse(in[1:], valueFlags)
		if !a.has("reinstall") || a.arg(0) != c.distro || a.flags["de"] != c.de || a.flags["edition"] != c.ed || a.flags["token"] != c.token {
			t.Errorf("%q: pos %v flags %v", c.cmd, a.pos, a.flags)
		}
	}
}
