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
		// #354 (#4213, Debian 13 at "Updating"): unverified lists in use.
		{[]string{"WARNING: The following packages cannot be authenticated!", "  libc6 libc-bin", "E: There were unauthenticated packages and -y was used without --allow-unauthenticated"}, KindUnauth},
		// #354: gpg rejecting a key made "in the future" is the phone's clock.
		{[]string{"gpgv: key 6ED0E7B82643E131 was created 913412 seconds in the future (time warp or clock problem)", "E: The repository 'http://deb.debian.org/debian trixie InRelease' is not signed."}, KindClock},
		// #377 (AX-4G45SJ, SM-T580 armhf, kernel 3.18): apt can't load its libraries.
		{[]string{"apt-get: /lib/arm-linux-gnueabihf/libapt-private.so.0.0: version `GLIBCXX_3.4.21' not found (required by apt-get)", "apt-get: /lib/arm-linux-gnueabihf/libapt-private.so.0.0: version `GLIBC_2.4' not found (required by /lib/arm-linux-gnueabihf/libapt-private.so.0.0)"}, KindGuestLibs},
		{[]string{"apt: symbol lookup error: /lib/arm-linux-gnueabihf/libapt-pkg.so.6.0: undefined symbol: _ZTVN10__cxxabiv117__class_type_infoE, version CXXABI_1.3", "E: Sub-process returned an error code"}, KindGuestLibs},
		// support #312: proot's fake root didn't take
		{[]string{"==> ERROR: pacman-key needs to be run as root for this operation."}, KindFakeRoot},
		{[]string{"E: Could not open lock file /var/lib/dpkg/lock-frontend - open (13: Permission denied)", "E: Unable to acquire the dpkg frontend lock (/var/lib/dpkg/lock-frontend), are you root?"}, KindFakeRoot},
		{[]string{"dpkg: error: requested operation requires superuser privilege"}, KindFakeRoot},
		{[]string{"E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 20256 (apt-get)"}, KindLock},
		{[]string{"E: Sub-process /usr/bin/dpkg returned an error code (1)"}, KindOther},
		{[]string{"E: The repository 'http://ftp.tu-chemnitz.de/pub/linux/ubuntu-ports resolute InRelease' is not signed."}, KindIntercepted},
		{[]string{"E: Failed to fetch http://http.kali.org/dists/kali-rolling/InRelease  Redirection loop encountered"}, KindMirror},
		{[]string{"Err:1 http://kali.download/kali kali-rolling InRelease", "  404  Not Found [IP: 1.2.3.4 80]", "E: The repository 'http://kali.download/kali kali-rolling Release' does not have a Release file."}, KindMirror},
		{[]string{"W: Failed to fetch https://eu.mirror.ionos.com/linux/distributions/kali/kali/dists/kali-rolling/InRelease  Certificate verification failed: The certificate is NOT trusted. The certificate issuer is unknown."}, KindConnect},
		{[]string{"E: Clearsigned file isn't valid, got 'NOSPLIT' (does the network require authentication?)"}, KindIntercepted},
		// support #471: gpgv died under proot, so "not signed" is not the network
		{[]string{"Err:1 http://ports.ubuntu.com/ubuntu-ports resolute InRelease", "  Sub-process /usr/bin/gpgv exited unexpectedly", "W: OpenPGP signature verification failed: http://ports.ubuntu.com/ubuntu-ports resolute InRelease: Sub-process /usr/bin/gpgv exited unexpectedly", "E: The repository 'http://ports.ubuntu.com/ubuntu-ports resolute InRelease' is not signed.", "E: Sub-process returned an error code"}, KindProotSyscall},
		{[]string{"sh: 0: getcwd() failed: Function not implemented", "E: Sub-process returned an error code"}, KindProotSyscall},
		{[]string{"proot error: unknown option '--sysvipc'", "fatal error: see `proot --help`."}, KindProotArgs},
		// Support #171 (Galaxy SM-E135F, armv8l, Play Termux): not an option problem.
		{[]string{`proot error: execve("/usr/bin/env"): No such file or directory`, "proot info: possible causes:", "fatal error: see `proot --help`.", "proot error: can't chmod '/data/data/com.termux/files/home/.andronix/tmp/proot-12566-d4IFWa': No such file or directory"}, KindProotExec},
		{[]string{"proot error: can't create glue rootfs", "fatal error: see `proot --help`."}, KindProotFailed},
		// #319: a proot SIGSEGV wins over the dpkg/reinstreq symptom it leaves.
		{[]string{"Unpacking openssl-provider-legacy (3.5.5-1ubuntu3.7) over (3.5.5-1ubuntu3.5) ...", "proot warning: signal 11 received from process -1", "E: dpkg was interrupted, you must manually run 'dpkg --configure -a' to correct the problem."}, KindProotCrash},
		{[]string{"proot warning: signal 11 received from process -1", "error: failed to synchronize all databases (unexpected error)"}, KindProotCrash},
		// Support #201 (TECNO KI7, Kali XFCE, a resumed install): a package
		// left half-unpacked; not "other", not the plain interrupted case.
		{[]string{"7 not fully installed or removed.", "dpkg: error processing package cpp-16-aarch64-linux-gnu (--configure):", " package is in a very bad inconsistent state; it should", " be reinstalled before attempting configuration", "E: Sub-process /usr/bin/dpkg returned an error code (1)"}, KindReinstreq},
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

// TestFailureSignature: the kinds that a far-behind phone clock causes, and
// pacman's PGP "invalid or corrupted package" (a hash_mismatch by kind) but
// not a plain checksum mismatch (#354).
func TestFailureSignature(t *testing.T) {
	for _, c := range []struct {
		lines []string
		want  bool
	}{
		{[]string{":: File /var/cache/pacman/pkg/glibc-2.42-1-aarch64.pkg.tar.xz is corrupted (invalid or corrupted package (PGP signature))."}, true},
		{[]string{"error: glibc: signature from \"Arch Linux ARM Build System <builder@archlinuxarm.org>\" is unknown trust", "error: failed to commit transaction (invalid or corrupted package)"}, true},
		{[]string{"E: Failed to fetch http://x/y.deb  Hash Sum mismatch"}, false},
		{[]string{"E: The repository 'http://deb.debian.org/debian trixie InRelease' is not signed."}, true},
		{[]string{"E: There were unauthenticated packages and -y was used without --allow-unauthenticated"}, true},
		{[]string{"E: Unable to locate package foo"}, false},
	} {
		f := Diagnose(c.lines, errors.New("exit status 1"))
		if got := f.Signature(); got != c.want {
			t.Errorf("%q: kind %s, Signature()=%v, want %v", c.lines, f.Kind, got, c.want)
		}
	}
}
