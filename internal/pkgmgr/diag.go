package pkgmgr

import (
	"context"
	"errors"
	"regexp"
	"strings"
)

// Why a package-manager run failed, read from its last lines (apt, dnf,
// pacman, xbps, apk), so it can be retried the right way and counted in
// telemetry (error_kind, error_detail). 126 failed installs of 600 in
// Oct 1-4 were mostly package_manager, with no way to see why.

type Kind string

const (
	KindDNS     Kind = "network_dns"
	KindConnect Kind = "network_connect"
	KindHTTP404 Kind = "http_404"
	KindHTTP5xx Kind = "http_5xx"
	KindHash    Kind = "hash_mismatch"
	KindClock   Kind = "clock_skew"
	KindGPG     Kind = "gpg"
	KindDpkg    Kind = "dpkg_interrupted"
	// A package dpkg left half-unpacked ("very bad inconsistent state; it
	// should be reinstalled"): an install stopped mid-unpack, then resumed
	// (support #201 #203: Kali XFCE, TECNO KI7). --configure -a can't fix it.
	KindReinstreq Kind = "dpkg_reinstreq"
	KindLock      Kind = "lock"
	// pacman's local database has an entry without its desc or files: a
	// pacman that was killed while unpacking that package (support #5412).
	// Fixed by reinstalling it (PacmanRepair).
	KindPacmanDB  Kind = "pacman_db"
	KindBroken    Kind = "broken_deps"
	KindDiskFull  Kind = "disk_full"
	KindKilled    Kind = "killed"
	KindCancelled Kind = "cancelled"
	// An apt hook (APT::Update::Post-Invoke, e.g. command-not-found's
	// database) failed, not the update itself (support, 2.0.3 Play Termux).
	KindHook Kind = "apt_hook"
	// "InRelease is not signed" on mirrors that are fine: the network
	// rewrote the plain-HTTP download (a Wi-Fi login page or a filter).
	KindIntercepted Kind = "network_intercepted"
	// proot refused one of its options ("unknown option", then "see
	// proot --help").
	KindProotArgs Kind = "proot_args"
	// The mirror sent apt in circles ("Redirection loop encountered") or
	// answered without its index ("does not have a Release file"): Kali's
	// kali.download and http.kali.org redirect to each other (support
	// #209-211, realme, Oct 2026).
	KindMirror Kind = "mirror_unusable"
	// proot ran but couldn't start any program in the distro: execve
	// failed (support #171 #124 #110: 32-bit phones, every exec ENOENT).
	KindProotExec Kind = "proot_exec"
	// proot stopped with another fatal error ("see proot --help" ends
	// every proot fatal error, not only option ones).
	KindProotFailed Kind = "proot_failed"
	// proot itself SIGSEGV'd mid-run ("proot warning: signal 11 received"),
	// crashing a package op and leaving it half-done — not the network, not
	// the distro. Seen on GKI 6.6/6.12 "android15/android16" kernels with
	// Termux's own proot build, across dpkg and pacman; the app's bundled
	// proot doesn't (card #319).
	KindProotCrash Kind = "proot_crash"
	// pacman-key couldn't make the local keyring: gpg-agent didn't start
	// under proot (support #119: Redmi Note 8 Pro, kernel 4.14).
	KindKeyring Kind = "keyring"
	// A program in the distro wasn't root under proot -0 ("pacman-key
	// needs to be run as root", "are you root?"): proot's fake root didn't
	// take (support #312: Arch, an Android 16 ROM on kernel 4.14).
	KindFakeRoot Kind = "fake_root"
	// Syscalls fail inside the distro under proot: the shell's getcwd()
	// gets ENOSYS and gpgv dies on a signal, so every InRelease reads
	// as "not signed" (support #470/#471: Blackview SHARK 8, Android 13,
	// kernel 5.10, Play Termux 2026.02.11). Not the network.
	KindProotSyscall Kind = "proot_syscall"
	// apt refused packages from lists it couldn't verify ("There were
	// unauthenticated packages", "cannot be authenticated"): the refresh's
	// signature check failed (a wrong clock, a network rewriting downloads,
	// a mirror mid-sync) yet older, unverified lists were still used (#354,
	// #4213: Debian 13 at "Updating", Nov 4).
	KindUnauth Kind = "unauthenticated"
	// The distro's own programs can't load their libraries: the dynamic
	// loader reports every symbol version missing, glibc's included
	// ("version `GLIBCXX_3.4.21' not found", "CXXABI_1.3", "GLIBC_2.4",
	// even apt's own APTPKG_6.0), or "symbol lookup error". Seen only on
	// 32-bit (armhf) Termux on kernel 3.18 (card #377: SM-T580, SM-G611F,
	// SM-J701F; Debian 13 and Ubuntu 24.04), at the first apt run. Not
	// the network and not a broken mirror; retrying can't change it.
	// musl's loader words it "Error relocating apk: uncompress: symbol not
	// found" (card #398: Alpine on the same phones, 11 installs in two
	// weeks, each failing in under half a second). Only apk's own load:
	// a trigger that can't find a library after a failed download is the
	// download's failure, not this.
	KindGuestLibs Kind = "guest_libs"
	// apt knows no version of a package it was asked for ("Package adduser
	// has no installation candidate", "Unable to locate package sudo"). For
	// a package every archive has, that is apt without package lists from
	// the server the sources name: 37 of 71 "Installing the basics" reports
	// in Nov 2026 (auto-ticket #1092, support #5586 #5593 #5783; Kali,
	// Debian and Ubuntu), each after a refresh that had passed. The lists
	// are fetched again before it counts as a missing package.
	KindNotFound Kind = "package_not_found"
	KindOther    Kind = "other"
)

// Most specific first: a 404 or a hash mismatch also prints "Failed to
// fetch", and a clock problem shows up as a signature error.
var patterns = []struct {
	kind Kind
	re   *regexp.Regexp
}{
	{KindProotSyscall, regexp.MustCompile(`(?i)getcwd\(\) failed: function not implemented|sub-process /usr/bin/(gpgv|sqv|gpg) exited unexpectedly`)},
	{KindDiskFull, regexp.MustCompile(`(?i)no space left on device|not enough free space|you don't have enough free space|insufficient disk space`)},
	{KindProotExec, regexp.MustCompile(`(?i)proot error: execve\(`)},
	{KindProotArgs, regexp.MustCompile(`(?i)proot error: .*(unknown|invalid|unrecognized|bad) option|option .*requires an argument`)},
	{KindProotFailed, regexp.MustCompile(`(?i)see .?proot --help`)},
	// Before the dpkg/reinstreq/hook symptoms: a proot SIGSEGV is the root
	// cause of the half-done package op it leaves behind (card #319).
	{KindProotCrash, regexp.MustCompile(`(?i)proot (warning|error|info):.*signal 11`)},
	{KindGuestLibs, regexp.MustCompile("(?i)version `(GLIBC|GLIBCXX|CXXABI|GCC|APTPKG)_[A-Z0-9_.]+' not found|symbol lookup error|^error relocating (\\S*/)?(apk|libapk\\.so[0-9.]*): .*symbol not found|^error loading shared library .*\\(needed by (\\S*/)?(apk|libapk\\.so[0-9.]*)\\)")},
	{KindHook, regexp.MustCompile(`(?i)problem executing scripts|post-invoke|^e: sub-process returned an error code$`)},
	{KindReinstreq, regexp.MustCompile(`(?i)very bad inconsistent state|should be reinstalled before|reinstreq`)},
	{KindDpkg, regexp.MustCompile(`(?i)dpkg was interrupted|dpkg --configure -a`)},
	{KindFakeRoot, regexp.MustCompile(`(?i)needs to be run as root|must be run as root|requires superuser privilege|unless you are root|you must be root|are you root\?`)},
	{KindLock, regexp.MustCompile(`(?i)could not get lock|unable to acquire the dpkg frontend lock|unable to lock database|db\.lck|waiting for cache lock`)},
	// gpg's own words for a clock behind a key's creation time (#354).
	{KindClock, regexp.MustCompile(`(?i)is not valid yet|not valid until|release file .* is expired|invalid for another|time warp or clock problem|created \d+ seconds? in the future`)},
	// Before hash_mismatch: pacman words a damaged local database entry as
	// "failed to prepare transaction (invalid or corrupted package)".
	{KindPacmanDB, regexp.MustCompile(`(?i)could not fully load metadata for package|could not open file /var/lib/pacman/local/|failed to prepare transaction \(invalid or corrupted package\)`)},
	{KindHash, regexp.MustCompile(`(?i)hash sum mismatch|file has unexpected size|hashes of expected file|mirror sync in progress|invalid or corrupted package|checksum doesn't match|sha256 mismatch|file integrity error`)},
	{KindMirror, regexp.MustCompile(`(?i)redirection loop|does not have a release file`)},
	{KindHTTP404, regexp.MustCompile(`(?i)\b404\s+not found|error 404|http/[0-9.]+ 404|http 404:`)},
	{KindHTTP5xx, regexp.MustCompile(`(?i)\b50[0-9]\s+(internal|bad gateway|service|gateway)|error 50[0-9]|http/[0-9.]+ 50[0-9]|http 50[0-9]:`)},
	// apk (apk-tools 3) words its own: "DNS: transient error (try again
	// later)" is musl giving up on resolv.conf's servers after 5 s; "could
	// not connect to server", "network error (check Internet connection and
	// firewall)" and the "TLS: ..." ones are the connection (card #398).
	{KindDNS, regexp.MustCompile(`(?i)temporary failure resolving|could not resolve|name or service not known|unknown host|no address associated|resolving timed out|dns: (transient error|non-recoverable failure|name does not exist|no address for host|address family for host)`)},
	{KindConnect, regexp.MustCompile(`(?i)could not connect|connection timed out|connection refused|connection failed|connection reset|network is unreachable|operation timed out|failed to connect|unable to connect|transfer failed|failed retrieving file|couldn't download|reposync|curl error|cannot download|errno 104|ssl connect error|tls handshake|certificate verify failed|certificate verification failed|no system certificates available|could not connect to server|network error \(check internet|network connection aborted|host is unreachable|temporary error \(try again later\)|tls: (handshake failed|unspecified error|server certificate not trusted|server hostname mismatch)`)},
	{KindKeyring, regexp.MustCompile(`(?i)gnupg agent unusable|agent_genkey failed|no agent running|no secret key available to sign with|can't connect to the gpg-agent`)},
	{KindGPG, regexp.MustCompile(`(?i)no_pubkey|invalid signature|expkeysig|badsig|gpg error|public key is not available|signature .* is (unknown|invalid)|key is unknown|(?-i:: (UNTRUSTED|BAD) signature)\s*$`)},
	// After GPG: "not signed" with a real key error is GPG; alone, the
	// download wasn't the mirror's (a login page or a filter).
	{KindIntercepted, regexp.MustCompile(`(?i)in?release'? is not signed|clearsigned file isn't valid|got noclearsign|NOSPLIT`)},
	{KindUnauth, regexp.MustCompile(`(?i)there were unauthenticated packages|packages cannot be authenticated|unauthenticated packages`)},
	{KindBroken, regexp.MustCompile(`(?i)unmet dependencies|held broken packages|broken packages|unresolvable|nothing provides|conflicting (files|requests)|cannot install both|unable to satisfy`)},
	// After the network kinds: the refresh's own ca-certificates install
	// says it too, below the lines of the server that didn't answer.
	{KindNotFound, regexp.MustCompile(`(?i)\bhas no installation candidate|\bunable to locate package\b`)},
	{KindKilled, regexp.MustCompile(`(?i)signal: killed|^killed$|signal 9|exit status 137`)},
}

// noReleaseRe is apt's "does not have a Release file", with the server's
// name. A mirror that answers without its index gets it (a 404:
// mirror_unusable), and so does a server apt never reached: apt 3 takes
// "Network is unreachable" for an answer, asks for the Release file next and
// ends with this line (exit status 100), where apt 2.8 warned "Failed to
// fetch" (Ubuntu 26.04 and Debian 13 with the phone offline, 2.1.6-rc2
// gates: one try, "The package mirrors sent a broken answer").
var noReleaseRe = regexp.MustCompile(`(?i)the repository '(?:[a-z][a-z0-9+.-]*://)?([^/\s':]+)[^']*' does not have a release file`)

// unreached turns a "does not have a Release file" failure into the lookup
// or connection failure the same run printed for that server: the index is
// missing because the server wasn't reached. Every server apt says it of
// must have such a line of its own (apt names the server in each). One that
// answered is a mirror without its index, whatever another source's server
// did, and so is a redirector whose target didn't answer (Kali's): that line
// names the target.
func unreached(f *Failure, all []string) {
	var hosts []string
	for _, l := range all {
		if m := noReleaseRe.FindStringSubmatch(l); m != nil {
			hosts = append(hosts, strings.ToLower(m[1]))
		}
	}
	if len(hosts) == 0 {
		return
	}
	kind, line := Kind(""), ""
	for i, h := range hosts {
		k, l := netFailureFor(all, h)
		if k == "" {
			return
		}
		if i == 0 {
			kind, line = k, l
		}
	}
	f.Kind, f.Detail, f.Line = kind, CleanDetail(line), line
}

// netFailureFor is the first lookup failure, else connection failure, among
// lines that names host.
func netFailureFor(lines []string, host string) (Kind, string) {
	for _, p := range patterns {
		if p.kind != KindDNS && p.kind != KindConnect {
			continue
		}
		for _, l := range lines {
			if strings.Contains(strings.ToLower(l), host) && !noReleaseRe.MatchString(l) && p.re.MatchString(l) {
				return p.kind, l
			}
		}
	}
	return "", ""
}

// errLineRe picks the line worth reporting when no pattern names one.
// apk reports a source it couldn't fetch as a warning ("WARNING: updating
// <url>: <why>") and ends with a count, so that line is the error.
var errLineRe = regexp.MustCompile(`(?i)^(e:|err:|error|w: (failed|some index)|fatal|ERROR:|warning: (updating|fetching|opening) )`)

// apkErrorRe is a line apk prints for something that failed (see errLineRe).
var apkErrorRe = regexp.MustCompile(`^(ERROR: |WARNING: (updating|fetching|opening) )`)

// ApkErrorLine reports whether l is one of apk's error lines. apk goes on
// with the other packages after one fails, so by the end of a big install
// the line that says why can be hundreds of lines back.
func ApkErrorLine(l string) bool { return apkErrorRe.MatchString(l) }

// Failure is a failed package-manager run, diagnosed.
type Failure struct {
	Kind   Kind
	Detail string // the error line, safe for telemetry
	Line   string // the same line as printed (local use only: paths, URLs)
	Err    error
	// Lists: what failed is the package lists' refresh, whichever command
	// ran into it (an install that found no package lists and couldn't get
	// them): the error is titled as a failed refresh.
	Lists bool
	// sigLine: some line of the run named a package signature (pacman's
	// "(PGP signature)", "signature from ... is unknown trust").
	sigLine bool
}

var sigLineRe = regexp.MustCompile(`(?i)pgp signature|signature from .* is (unknown|invalid|marginal)|unknown trust`)

func (f *Failure) Error() string {
	if f.Detail != "" {
		return f.Detail
	}
	return f.Err.Error()
}
func (f *Failure) Unwrap() error { return f.Err }

// Signature reports whether the failure is a package-signature check:
// these all fail on a phone whose clock is far behind (gpg rejects keys
// "created in the future"), whatever the mirror (#354).
func (f *Failure) Signature() bool {
	switch f.Kind {
	case KindGPG, KindIntercepted, KindClock, KindUnauth:
		return true
	case KindHash:
		return f.sigLine
	}
	return false
}

// network reports whether another mirror or a wait might fix it.
func (k Kind) Network() bool {
	return k == KindDNS || k == KindConnect || k == KindHTTP5xx
}

// diagnose names the failure from the run's last lines and its error.
func Diagnose(lines []string, err error) *Failure {
	f := &Failure{Kind: KindOther, Err: err}
	if errors.Is(err, context.Canceled) {
		f.Kind = KindCancelled
		return f
	}
	all := append(append([]string{}, lines...), err.Error())
	for _, l := range all {
		if sigLineRe.MatchString(l) {
			f.sigLine = true
			break
		}
	}
	for _, p := range patterns {
		for _, l := range all {
			if p.re.MatchString(l) {
				f.Kind, f.Detail, f.Line = p.kind, CleanDetail(l), l
				if p.kind == KindMirror && noReleaseRe.MatchString(l) {
					unreached(f, all)
				}
				return f
			}
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		if errLineRe.MatchString(strings.TrimSpace(lines[i])) {
			f.Detail = CleanDetail(lines[i])
			return f
		}
	}
	f.Detail = CleanDetail(err.Error())
	return f
}

var (
	// A user name and password (or a token) in a URL, a private package
	// source: everything from the scheme to the last '@' of the word. That
	// takes a path with an '@' in it along (the "host" is then what follows
	// it): a wrong host in telemetry rather than part of a password that
	// has a '/' or a quote in it.
	urlCredRe = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)\S*@`)
	// What is left with an '@' in it: user@host with no scheme, an e-mail
	// address. Only what follows the '@' stays.
	atRe    = regexp.MustCompile(`\S+@`)
	urlRe   = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://([^/\s'"\]]+)[^\s'"\]]*`)
	ipRe    = regexp.MustCompile(`\[IP: [^\]]*\]|\b\d{1,3}(\.\d{1,3}){3}\b|\b[0-9a-fA-F:]{8,}:[0-9a-fA-F:]+\b`)
	pathRe  = regexp.MustCompile(`(^|[\s'"(=])/[^\s'"]+`)
	notText = regexp.MustCompile(`[^A-Za-z0-9 ._:,+/()-]+`)
	spaces  = regexp.MustCompile(`\s+`)
)

// cleanDetail makes an error line safe to send: URLs become their host
// (without the user name and password a private source's URL carries), IP
// addresses and file paths go, and only the characters products-api accepts
// for text props stay; at most 120 of them.
func CleanDetail(s string) string {
	s = urlCredRe.ReplaceAllString(s, "$1")
	s = urlRe.ReplaceAllString(s, "$1")
	s = atRe.ReplaceAllString(s, "")
	s = ipRe.ReplaceAllString(s, "")
	s = pathRe.ReplaceAllString(s, "$1(path)")
	s = notText.ReplaceAllString(s, " ")
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	if len(s) > 120 {
		s = strings.TrimSpace(s[:120])
	}
	return s
}
