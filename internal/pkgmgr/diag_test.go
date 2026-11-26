package pkgmgr

import (
	"errors"
	"strings"
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
		// 2.1.6-rc2 gates: apt 3 offline. "Network is unreachable" isn't one of
		// the failures apt 3 only warns about: it asks for the Release file
		// next and ends with the line a mirror without its index gets (Ubuntu
		// 26.04 on the Redmi; apt 3.0.3 in Debian 13 prints the same).
		{[]string{"Ign:1 http://ports.ubuntu.com/ubuntu-ports resolute InRelease", "Err:5 http://ports.ubuntu.com/ubuntu-ports resolute Release", "  Cannot initiate the connection to ports.ubuntu.com:80 (2620:2d:4002:1::10b). - connect (101: Network is unreachable) Cannot initiate the connection to ports.ubuntu.com:80 (91.189.91.104). - connect (101: Network is unreachable)", "Reading package lists...", "E: The repository 'http://ports.ubuntu.com/ubuntu-ports resolute Release' does not have a Release file."}, KindConnect},
		{[]string{"Err:4 http://deb.debian.org/debian stable Release", "  Cannot initiate the connection to deb.debian.org:80 (192.0.2.5). - connect (101: Network is unreachable)", "E: The repository 'http://deb.debian.org/debian stable Release' does not have a Release file."}, KindConnect},
		{[]string{"Err:4 http://deb.debian.org/debian stable Release", "  Temporary failure resolving 'deb.debian.org'", "E: The repository 'http://deb.debian.org/debian stable Release' does not have a Release file."}, KindDNS},
		// A redirect loop is the mirror's whatever else the run printed.
		{[]string{"  Could not connect to kali.download:80 (104.17.253.239), connection timed out", "E: Failed to fetch http://http.kali.org/dists/kali-rolling/InRelease  Redirection loop encountered"}, KindMirror},
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

// TestPacmanDBDamaged: a local database entry without desc/files (pacman
// killed while unpacking, support #5412) is pacman_db, not hash_mismatch,
// whichever of pacman's three lines is in the tail; a package that really
// fails its signature stays a hash_mismatch with Signature().
func TestPacmanDBDamaged(t *testing.T) {
	for _, lines := range [][]string{
		{"error: failed to prepare transaction (invalid or corrupted package)"},
		{"warning: could not fully load metadata for package vim-runtime-9.2.0849-1", "error: failed to prepare transaction (invalid or corrupted package)"},
		{"error: could not open file /var/lib/pacman/local/glib2-2.90.1-1/desc: No such file or directory", "error: failed retrieving file 'core.db' from mirror.archlinuxarm.org : Connection timed out"},
	} {
		if f := Diagnose(lines, errors.New("exit status 1")); f.Kind != KindPacmanDB || f.Signature() {
			t.Errorf("%q: kind %s Signature()=%v, want %s and no signature", lines, f.Kind, f.Signature(), KindPacmanDB)
		}
	}
	for _, lines := range [][]string{
		{":: File /var/cache/pacman/pkg/glibc-2.42-1-aarch64.pkg.tar.xz is corrupted (invalid or corrupted package (PGP signature)).", "error: failed to commit transaction (invalid or corrupted package (PGP signature))"},
		{"error: glibc: signature from \"Arch Linux ARM Build System <builder@archlinuxarm.org>\" is unknown trust", "error: failed to commit transaction (invalid or corrupted package)"},
	} {
		if f := Diagnose(lines, errors.New("exit status 1")); f.Kind != KindHash || !f.Signature() {
			t.Errorf("%q: kind %s Signature()=%v, want %s with a signature", lines, f.Kind, f.Signature(), KindHash)
		}
	}
}

// TestDiagnoseApk: apk-tools 3 (Alpine 3.24) as it prints, captured from
// our image on 2026-11-12 (card #398). A source it can't fetch is a
// "WARNING: updating ..." line and an exit status that counts the failed
// sources; nothing says "ERROR", so these all read "other: exit status 2".
func TestDiagnoseApk(t *testing.T) {
	main := "https://dl-cdn.alpinelinux.org/alpine/v3.24/main/aarch64/APKINDEX.tar.gz"
	for _, c := range []struct {
		lines  []string
		want   Kind
		detail string
	}{
		{[]string{"WARNING: updating " + main + ": DNS: transient error (try again later)",
			"WARNING: updating https://dl-cdn.alpinelinux.org/alpine/v3.24/community/aarch64/APKINDEX.tar.gz: DNS: transient error (try again later)",
			"0 unavailable, 2 stale; 28581 distinct packages available"}, KindDNS, "WARNING: updating dl-cdn.alpinelinux.org DNS: transient error (try again later)"},
		// A package download (telemetry, 6 Nov, an in-app XFCE install).
		{[]string{"(1/3) Installing xvfb (21.1.25-r0)", "ERROR: xvfb-21.1.25-r0: DNS: transient error (try again later)", "1 error; 8435 KiB in 16 packages"}, KindDNS, "ERROR: xvfb-21.1.25-r0: DNS: transient error (try again later)"},
		{[]string{"WARNING: updating " + main + ": DNS: name does not exist"}, KindDNS, ""},
		{[]string{"WARNING: updating and opening " + main + ": Connection refused", "2 unavailable, 0 stale; 16 distinct packages available"}, KindConnect, ""},
		{[]string{"WARNING: updating and opening " + main + ": TLS: handshake failed (client cert needed?)"}, KindConnect, ""},
		{[]string{"WARNING: updating " + main + ": TLS: server certificate not trusted"}, KindConnect, ""},
		{[]string{"WARNING: updating " + main + ": could not connect to server (check repositories file)"}, KindConnect, ""},
		{[]string{"WARNING: updating " + main + ": network error (check Internet connection and firewall)"}, KindConnect, ""},
		{[]string{"WARNING: updating " + main + ": temporary error (try again later)"}, KindConnect, ""},
		{[]string{"WARNING: updating " + main + ": operation timed out"}, KindConnect, ""},
		{[]string{"WARNING: updating " + main + ": HTTP 404: Not Found"}, KindHTTP404, ""},
		{[]string{"WARNING: updating " + main + ": HTTP 503: Service Unavailable"}, KindHTTP5xx, ""},
		{[]string{"WARNING: updating " + main + ": UNTRUSTED signature"}, KindGPG, ""},
		{[]string{"ERROR: nano-9.3-r0: BAD signature"}, KindGPG, ""},
		{[]string{"ERROR: nano-9.3-r0: file integrity error"}, KindHash, ""},
		{[]string{"ERROR: Unable to lock database: Resource temporarily unavailable"}, KindLock, ""},
		{[]string{"ERROR: Failed to create usr/lib/libxul.so: No space left on device"}, KindDiskFull, ""},
		// musl's loader, 32-bit Termux on kernel 3.18 (11 installs, 28 Oct-12 Nov).
		{[]string{"Error relocating apk: uncompress: symbol not found"}, KindGuestLibs, "Error relocating apk: uncompress: symbol not found"},
		{[]string{"Error loading shared library libz.so.1: No such file or directory (needed by /sbin/apk)"}, KindGuestLibs, ""},
		{[]string{"Error relocating /usr/lib/libapk.so.3.0.0: inflateInit2_: symbol not found"}, KindGuestLibs, ""},
		{[]string{"WARNING: updating " + main + ": Host is unreachable"}, KindConnect, ""},
		// A trigger that can't load a library whose download failed is the
		// download's failure, not guest_libs (review).
		{[]string{"ERROR: glib-2.84.4-r0: DNS: transient error (try again later)", "Executing gtk-update-icon-cache-3.24.49-r0.trigger",
			"Error loading shared library libglib-2.0.so.0: No such file or directory (needed by /usr/bin/gtk-update-icon-cache)",
			"Error relocating /usr/bin/gtk-update-icon-cache: g_free: symbol not found", "1 error; 100 MiB in 300 packages"}, KindDNS, ""},
		// A reason we don't know yet still reaches telemetry and the report.
		{[]string{"WARNING: updating " + main + ": something new", "1 unavailable, 0 stale; 16 distinct packages available"}, KindOther, "WARNING: updating dl-cdn.alpinelinux.org something new"},
		// A run that worked up to a real error keeps apk's own ERROR line.
		{[]string{"WARNING: opening /var/cache/apk: No such file or directory", "ERROR: unable to select packages:", "  nosuchpkg (no such package):"}, KindOther, "ERROR: unable to select packages:"},
	} {
		f := Diagnose(c.lines, errors.New("exit status 2"))
		if f.Kind != c.want {
			t.Errorf("%q: %s, want %s", c.lines[0], f.Kind, c.want)
		}
		if c.detail != "" && f.Detail != c.detail {
			t.Errorf("%q: detail %q, want %q", c.lines[0], f.Detail, c.detail)
		}
	}
	// An untrusted signature is a signature failure (the clock check applies).
	if f := Diagnose([]string{"ERROR: nano-9.3-r0: UNTRUSTED signature"}, errors.New("exit status 1")); !f.Signature() {
		t.Errorf("UNTRUSTED signature: kind %s, Signature() false", f.Kind)
	}
	// gpg's and rpm's words for a bad signature are not apk's: they keep
	// the kind they had (review).
	for _, l := range []string{"gpg: key 3B94A80E50A477C7: 1 bad signature", "gpg: BAD signature from \"X <x@example.org>\" [unknown]", "error: GPGME error: Bad signature", "pkg.rpm: digests SIGNATURES NOT OK: Bad signature"} {
		if f := Diagnose([]string{l}, errors.New("exit status 1")); f.Kind != KindOther {
			t.Errorf("%q: %s, want other as before", l, f.Kind)
		}
	}
	if !ApkErrorLine("ERROR: nano-9.3-r0: BAD signature") || !ApkErrorLine("WARNING: updating "+main+": x") || ApkErrorLine("(1/3) Installing nano (9.3-r0)") || ApkErrorLine("E: Unable to locate package x") {
		t.Error("ApkErrorLine")
	}
	// Network kinds are retried with a pause; the others here are not network.
	if !KindDNS.Network() || !KindConnect.Network() || KindGuestLibs.Network() {
		t.Error("Network() changed")
	}
}

// No user name, password or token from a URL reaches telemetry (review of
// rc-2.1.6): apk prints a private source's address as the repositories file
// has it, and only the host may stay.
func TestCleanDetailDropsURLCredentials(t *testing.T) {
	secrets := []string{"bob", "s3cret", "tok_9f8e7d", "w0rd", "p@ss", "tok3n", "user", "pw", "s3", "cret"}
	for in, want := range map[string]string{
		"WARNING: updating https://bob:s3cret@repo.example.org/alpine/main/aarch64/APKINDEX.tar.gz: HTTP 403: Forbidden": "WARNING: updating repo.example.org HTTP 403: Forbidden",
		"Err:1 http://tok_9f8e7d@apt.example.net/debian stable InRelease":                                                "Err:1 apt.example.net stable InRelease",
		// A password with an '@' of its own, an empty user name, a port.
		"error: failed retrieving file from https://bob:p@ss:w0rd@mirror.example.org:8443/arch/core.db": "error: failed retrieving file from mirror.example.org:8443",
		"E: Failed to fetch https://:tok3n@deb.example.com/dists/stable/InRelease  401  Unauthorized":   "E: Failed to fetch deb.example.com 401 Unauthorized",
		"curl: (22) 'https://user:pw@dl.example.org/x.tar.xz' 403":                                      "curl: (22) dl.example.org 403",
		// What a URL shouldn't have but a mistyped sources file can: a
		// scheme in capitals, a quote, a bracket or a '/' in the password,
		// nothing after the '@'.
		"WARNING: updating HTTPS://bob:s3cret@repo.example.org/alpine/main: HTTP 403":  "WARNING: updating repo.example.org HTTP 403",
		`WARNING: updating https://bob:s3"cret@repo.example.org/alpine/main: HTTP 403`: "WARNING: updating repo.example.org HTTP 403",
		"WARNING: updating https://bob:s3]cret@repo.example.org/alpine/main: HTTP 403": "WARNING: updating repo.example.org HTTP 403",
		"WARNING: updating https://bob:s3/cret@repo.example.org/alpine/main: HTTP 403": "WARNING: updating repo.example.org HTTP 403",
		"WARNING: updating https://bob:s3cret@: could not connect to server":           "WARNING: updating : could not connect to server",
		"E: Failed to fetch https://deb.example.com/x.deb,mail=bob@example.org  404":   "E: Failed to fetch example.org 404",
		// No scheme, an e-mail address: only what follows the '@' stays.
		"fatal: unable to access bob:s3cret@git.example.org: could not connect":                       "fatal: unable to access git.example.org: could not connect",
		`error: archlinux-keyring: signature from "A Packager <user@archlinux.org>" is unknown trust`: "error: archlinux-keyring: signature from A Packager archlinux.org is unknown trust",
		// No credentials: as before. An '@' in the path reads as one: the
		// host named is then wrong, and nothing of the URL before it stays.
		"E: Failed to fetch http://ports.ubuntu.com/ubuntu-ports/pool/main/x/x.deb  404  Not Found": "E: Failed to fetch ports.ubuntu.com 404 Not Found",
		"GET https://repo.example.org/pool/@scope/x.apk 404":                                        "GET scope 404",
		"WARNING: updating https://dl-cdn.alpinelinux.org/alpine/v3.24/main: DNS: transient error":  "WARNING: updating dl-cdn.alpinelinux.org DNS: transient error",
		"E: Failed to fetch mirror+https://deb.example.com/dists/stable/InRelease  404":             "E: Failed to fetch deb.example.com 404",
	} {
		got := CleanDetail(in)
		if got != want {
			t.Errorf("%q:\n got %q\nwant %q", in, got, want)
		}
		for _, bad := range secrets {
			if strings.Contains(in, bad) && strings.Contains(got, bad) {
				t.Errorf("%q: %q left in %q", in, bad, got)
			}
		}
	}
	// The detail of a diagnosed failure is the cleaned line.
	f := Diagnose([]string{"WARNING: updating and opening https://bob:s3cret@repo.example.org/alpine/main/aarch64/APKINDEX.tar.gz: Operation timed out"}, errors.New("exit status 1"))
	if f.Kind != KindConnect || strings.Contains(f.Detail, "s3cret") || strings.Contains(f.Detail, "bob") || !strings.Contains(f.Detail, "repo.example.org") {
		t.Errorf("Diagnose: %s %q", f.Kind, f.Detail)
	}
}

// The failure names the line that says why the server wasn't reached, not
// apt's "does not have a Release file" (the error the user reads).
func TestDiagnoseUnreachedDetail(t *testing.T) {
	f := Diagnose([]string{
		"Err:5 http://ports.ubuntu.com/ubuntu-ports resolute Release",
		"  Cannot initiate the connection to ports.ubuntu.com:80 (91.189.91.104). - connect (101: Network is unreachable)",
		"E: The repository 'http://ports.ubuntu.com/ubuntu-ports resolute Release' does not have a Release file.",
	}, errors.New("exit status 100"))
	if f.Kind != KindConnect || !strings.Contains(f.Detail, "Network is unreachable") || strings.Contains(f.Detail, "Release file") || !f.Kind.Network() {
		t.Errorf("%s: %q", f.Kind, f.Detail)
	}
	// Review of #9: the connection failure must be that server's. Here the
	// distro's mirror answered without its index (a 404) and another
	// source's server refused the connection: the mirror, as before.
	mixed := []string{
		"Err:3 http://kali.download/kali kali-rolling Release",
		"  404  Not Found [IP: 104.17.253.239 80]",
		"Err:4 http://repo.example.org/apt stable Release",
		"  Could not connect to repo.example.org:80 (203.0.113.7). - connect (111: Connection refused)",
		"E: The repository 'http://kali.download/kali kali-rolling Release' does not have a Release file.",
		"E: The repository 'http://repo.example.org/apt stable Release' does not have a Release file.",
	}
	if f := Diagnose(mixed, errors.New("exit status 100")); f.Kind != KindMirror {
		t.Errorf("a mirror that answered 404 next to an unreachable third-party server: %s", f.Kind)
	}
	// Kali's redirector answered and its target didn't: the mirror.
	if f := Diagnose([]string{"Err:1 http://http.kali.org/kali kali-rolling Release", "  Could not connect to mirror.example.net:80 (203.0.113.9), connection timed out", "E: The repository 'http://http.kali.org/kali kali-rolling Release' does not have a Release file."}, errors.New("exit status 100")); f.Kind != KindMirror {
		t.Errorf("a redirector whose target didn't answer: %s", f.Kind)
	}
	// Two servers, neither reached: the network, named from the first.
	f = Diagnose([]string{
		"  Cannot initiate the connection to deb.debian.org:80 (192.0.2.5). - connect (101: Network is unreachable)",
		"  Temporary failure resolving 'repo.example.org'",
		"E: The repository 'http://deb.debian.org/debian stable Release' does not have a Release file.",
		"E: The repository 'https://repo.example.org/apt stable Release' does not have a Release file.",
	}, errors.New("exit status 100"))
	if f.Kind != KindConnect || !strings.Contains(f.Detail, "deb.debian.org") {
		t.Errorf("two unreached servers: %s %q", f.Kind, f.Detail)
	}
	// The mirror answered (a 404): still the mirror, with apt's line.
	f = Diagnose([]string{"Err:1 http://kali.download/kali kali-rolling InRelease", "  404  Not Found [IP: 1.2.3.4 80]", "E: The repository 'http://kali.download/kali kali-rolling Release' does not have a Release file."}, errors.New("exit status 100"))
	if f.Kind != KindMirror || !strings.Contains(f.Detail, "does not have a Release file") {
		t.Errorf("%s: %q", f.Kind, f.Detail)
	}
}

// TestNotFound: apt knows no version of a package it was asked for (the
// "Installing the basics" reports of Nov 2026, auto-ticket #1092): its own
// kind, so the lists are fetched again. Lines of a server that didn't answer
// in the same output win: that is the cause.
func TestNotFound(t *testing.T) {
	exit := errors.New("exit status 100")
	for _, c := range []struct {
		lines []string
		want  Kind
	}{
		{[]string{"Package adduser is not available, but is referred to by another package.", "This may mean that the package is missing, has been obsoleted, or", "is only available from another source", "", "E: Package 'adduser' has no installation candidate"}, KindNotFound},
		{[]string{"E: Package adduser has no installation candidate"}, KindNotFound}, // as the reports have it (the scrubber drops the quotes)
		{[]string{"Reading package lists...", "Building dependency tree...", "E: Unable to locate package sudo"}, KindNotFound},
		{[]string{"W: Failed to fetch http://http.kali.org/kali/dists/kali-rolling/InRelease  Temporary failure resolving 'http.kali.org'", "E: Unable to locate package ca-certificates"}, KindDNS},
		{[]string{"W: Failed to fetch http://deb.debian.org/debian/dists/trixie/InRelease  Unable to connect to deb.debian.org:80:", "E: Package 'ca-certificates' has no installation candidate"}, KindConnect},
		{[]string{"E: Unable to locate package x", "E: Unable to correct problems, you have held broken packages."}, KindBroken},
		{[]string{"Setting up adduser (3.152) ...", "E: Sub-process /usr/bin/dpkg returned an error code (1)"}, KindOther},
	} {
		if f := Diagnose(c.lines, exit); f.Kind != c.want {
			t.Errorf("%q: kind %s, want %s", c.lines[len(c.lines)-1], f.Kind, c.want)
		} else if c.want == KindNotFound && (f.Detail == "" || f.Lists) {
			t.Errorf("%q: detail %q, Lists %v", c.lines[len(c.lines)-1], f.Detail, f.Lists)
		}
	}
	if KindNotFound.Network() {
		t.Error("package_not_found isn't a network kind by itself")
	}
}
