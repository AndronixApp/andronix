package pkgmgr

import (
	"errors"
	"testing"
)

func TestDiagnoseSupportKinds(t *testing.T) {
	for _, c := range []struct {
		lines []string
		want  Kind
	}{
		{[]string{"Reading package lists...", "E: Problem executing scripts APT::Update::Post-Invoke-Success 'if /usr/bin/test -w /var/lib/command-not-found/ ...'", "E: Sub-process returned an error code"}, KindHook},
		{[]string{"E: Sub-process returned an error code"}, KindHook},
		{[]string{"E: Sub-process /usr/bin/dpkg returned an error code (1)"}, KindOther},
		{[]string{"E: The repository 'http://ftp.tu-chemnitz.de/pub/linux/ubuntu-ports resolute InRelease' is not signed."}, KindIntercepted},
		{[]string{"E: Clearsigned file isn't valid, got 'NOSPLIT' (does the network require authentication?)"}, KindIntercepted},
		{[]string{"proot error: unknown option '--sysvipc'", "fatal error: see `proot --help`."}, KindProotArgs},
		{[]string{"W: GPG error: http://deb.debian.org trixie InRelease: The following signatures couldn't be verified because the public key is not available: NO_PUBKEY 123"}, KindGPG},
	} {
		if got := Diagnose(c.lines, errors.New("exit status 100")); got.Kind != c.want {
			t.Errorf("%q: %s, want %s", c.lines[len(c.lines)-1], got.Kind, c.want)
		}
	}
}
