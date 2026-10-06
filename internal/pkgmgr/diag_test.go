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

// Support #119 (Arch, kernel 4.14): gpg-agent never starts, so
// pacman-key --init makes no master key. That's the keyring, not the
// network and not a bad signature.
func TestDiagnoseKeyring(t *testing.T) {
	lines := []string{
		"gpg: starting migration from earlier GnuPG versions",
		"gpg: can't connect to the gpg-agent: IPC connect call failed",
		"gpg: error: GnuPG agent unusable. Please check that a GnuPG agent can be started.",
		"gpg: agent_genkey failed: No agent running",
		"==> ERROR: There is no secret key available to sign with.",
		"==> Use 'pacman-key --init' to generate a default secret key.",
	}
	if got := Diagnose(lines, errors.New("exit status 1")); got.Kind != KindKeyring {
		t.Errorf("got %s (%q), want keyring", got.Kind, got.Detail)
	}
	// A real signature problem stays gpg.
	if got := Diagnose([]string{"error: archlinuxarm: signature from \"X\" is invalid"}, errors.New("exit status 1")); got.Kind != KindGPG {
		t.Errorf("bad signature: got %s", got.Kind)
	}
}
