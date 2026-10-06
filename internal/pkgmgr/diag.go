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
	KindDNS       Kind = "network_dns"
	KindConnect   Kind = "network_connect"
	KindHTTP404   Kind = "http_404"
	KindHTTP5xx   Kind = "http_5xx"
	KindHash      Kind = "hash_mismatch"
	KindClock     Kind = "clock_skew"
	KindGPG       Kind = "gpg"
	KindDpkg      Kind = "dpkg_interrupted"
	KindLock      Kind = "lock"
	KindBroken    Kind = "broken_deps"
	KindDiskFull  Kind = "disk_full"
	KindKilled    Kind = "killed"
	KindCancelled Kind = "cancelled"
	KindOther     Kind = "other"
)

// Most specific first: a 404 or a hash mismatch also prints "Failed to
// fetch", and a clock problem shows up as a signature error.
var patterns = []struct {
	kind Kind
	re   *regexp.Regexp
}{
	{KindDiskFull, regexp.MustCompile(`(?i)no space left on device|not enough free space|you don't have enough free space|insufficient disk space`)},
	{KindDpkg, regexp.MustCompile(`(?i)dpkg was interrupted|dpkg --configure -a`)},
	{KindLock, regexp.MustCompile(`(?i)could not get lock|unable to acquire the dpkg frontend lock|unable to lock database|db\.lck|waiting for cache lock`)},
	{KindClock, regexp.MustCompile(`(?i)is not valid yet|not valid until|release file .* is expired|invalid for another`)},
	{KindHash, regexp.MustCompile(`(?i)hash sum mismatch|file has unexpected size|hashes of expected file|mirror sync in progress|invalid or corrupted package|checksum doesn't match|sha256 mismatch`)},
	{KindHTTP404, regexp.MustCompile(`(?i)\b404\s+not found|error 404|http/[0-9.]+ 404`)},
	{KindHTTP5xx, regexp.MustCompile(`(?i)\b50[0-9]\s+(internal|bad gateway|service|gateway)|error 50[0-9]|http/[0-9.]+ 50[0-9]`)},
	{KindDNS, regexp.MustCompile(`(?i)temporary failure resolving|could not resolve|name or service not known|unknown host|no address associated|resolving timed out`)},
	{KindConnect, regexp.MustCompile(`(?i)could not connect|connection timed out|connection refused|connection failed|connection reset|network is unreachable|operation timed out|failed to connect|unable to connect|transfer failed|failed retrieving file|couldn't download|reposync|curl error|cannot download|errno 104|ssl connect error|tls handshake|certificate verify failed`)},
	{KindGPG, regexp.MustCompile(`(?i)no_pubkey|is not signed|invalid signature|expkeysig|badsig|gpg error|public key is not available|signature .* is (unknown|invalid)|key is unknown`)},
	{KindBroken, regexp.MustCompile(`(?i)unmet dependencies|held broken packages|broken packages|unresolvable|nothing provides|conflicting (files|requests)|cannot install both|unable to satisfy`)},
	{KindKilled, regexp.MustCompile(`(?i)signal: killed|^killed$|signal 9|exit status 137`)},
}

// errLineRe picks the line worth reporting when no pattern names one.
var errLineRe = regexp.MustCompile(`(?i)^(e:|err:|error|w: (failed|some index)|fatal|ERROR:)`)

// Failure is a failed package-manager run, diagnosed.
type Failure struct {
	Kind   Kind
	Detail string // the error line, safe for telemetry
	Err    error
}

func (f *Failure) Error() string {
	if f.Detail != "" {
		return f.Detail
	}
	return f.Err.Error()
}
func (f *Failure) Unwrap() error { return f.Err }

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
	for _, p := range patterns {
		for _, l := range all {
			if p.re.MatchString(l) {
				f.Kind, f.Detail = p.kind, CleanDetail(l)
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
	urlRe   = regexp.MustCompile(`[a-z][a-z0-9+.-]*://([^/\s'"\]]+)[^\s'"\]]*`)
	ipRe    = regexp.MustCompile(`\[IP: [^\]]*\]|\b\d{1,3}(\.\d{1,3}){3}\b|\b[0-9a-fA-F:]{8,}:[0-9a-fA-F:]+\b`)
	pathRe  = regexp.MustCompile(`(^|[\s'"(=])/[^\s'"]+`)
	notText = regexp.MustCompile(`[^A-Za-z0-9 ._:,+/()-]+`)
	spaces  = regexp.MustCompile(`\s+`)
)

// cleanDetail makes an error line safe to send: URLs become their host,
// IP addresses and file paths go, and only the characters products-api
// accepts for text props stay; at most 120 of them.
func CleanDetail(s string) string {
	s = urlRe.ReplaceAllString(s, "$1")
	s = ipRe.ReplaceAllString(s, "")
	s = pathRe.ReplaceAllString(s, "$1(path)")
	s = notText.ReplaceAllString(s, " ")
	s = strings.TrimSpace(spaces.ReplaceAllString(s, " "))
	if len(s) > 120 {
		s = strings.TrimSpace(s[:120])
	}
	return s
}
