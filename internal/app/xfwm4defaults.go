package app

import (
	"bufio"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// xfwm4 keeps its defaults in /usr/share/xfwm4/defaults. In a home without
// its own xfwm4 channel it writes every one of them into xfconf, one call
// at a time, on its first start, and xfdesktop waits behind that: 15-20 s
// of the first desktop start (Debian 13, no system xfwm4.xml; measured on
// an API 36 emulator and Device Farm phones, sv2-startup). A system channel
// with the same values is enough for xfwm4 not to write them: no per-user
// state, and nothing changes for anyone (they are xfwm4's own defaults,
// and a user's own channel still wins).
//
// The light profile's and safe mode's layers copy the system channel
// (writeXfconf starts from root/etc/xdg), so they carry these defaults plus
// their own overrides too; that's intended, keep it.

const (
	xfwm4DefaultsFile = "usr/share/xfwm4/defaults"
	xfwm4SystemFile   = "etc/xdg/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml"
	// xfwm4Marker says the system channel is ours, and from which defaults
	// (their SHA-256): regenerated when xfwm4's defaults change; a system
	// channel without it is the distro's own and never touched.
	xfwm4Marker = "etc/andronix/xfwm4-defaults.sha256"
)

// xfwm4StringKeys are the defaults xfwm4 keeps as strings although their
// value reads like a bool (as its first start writes them).
var xfwm4StringKeys = map[string]bool{"title_shadow_active": true, "title_shadow_inactive": true}

var (
	reInt    = regexp.MustCompile(`^-?[0-9]+$`)
	reDouble = regexp.MustCompile(`^-?[0-9]*\.[0-9]+$`)
)

// xfwm4Defaults are xfwm4's defaults (the file's contents) as xfconf
// entries ("xfwm4:/general/<key>:<type>:<value>").
func xfwm4Defaults(b []byte) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		l := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(l, "=")
		if !ok || strings.HasPrefix(l, "#") || k == "" || strings.ContainsAny(k, " /:") {
			continue
		}
		typ := "string"
		switch {
		case xfwm4StringKeys[k]:
		case v == "true" || v == "false":
			typ = "bool"
		case reInt.MatchString(v):
			typ = "int"
		case reDouble.MatchString(v):
			typ = "double"
		}
		out = append(out, "xfwm4:/general/"+k+":"+typ+":"+v)
	}
	return out
}

// writeXfwm4Defaults keeps the system xfwm4 channel generated from xfwm4's
// defaults: written when the distro has none, regenerated when ours came
// from other defaults (an xfwm4 upgrade), and a distro's own left alone.
// It returns what it did, for the log.
func writeXfwm4Defaults(root string) string {
	b, err := os.ReadFile(filepath.Join(root, xfwm4DefaultsFile))
	if err != nil {
		return "no xfwm4"
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(b))
	sys := filepath.Join(root, xfwm4SystemFile)
	marker := filepath.Join(root, xfwm4Marker)
	old, merr := os.ReadFile(marker)
	if _, err := os.Stat(sys); err == nil {
		switch {
		case merr != nil:
			return "distro's own kept"
		case strings.TrimSpace(string(old)) == sum:
			return "up to date"
		}
		os.Remove(sys) // ours, from older defaults: start over, so removed keys go too
	}
	entries := xfwm4Defaults(b)
	if len(entries) == 0 {
		return "no defaults in " + xfwm4DefaultsFile
	}
	if err := writeXfconf(root, filepath.Join(root, "etc/xdg"), entries); err != nil {
		return "failed: " + err.Error()
	}
	os.MkdirAll(filepath.Dir(marker), 0o755)
	if err := os.WriteFile(marker, []byte(sum+"\n"), 0o644); err != nil {
		return "failed: " + err.Error()
	}
	return fmt.Sprintf("generated %d keys", len(entries))
}
