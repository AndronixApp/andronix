package main

import (
	"regexp"
	"strings"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

// The app's copied command is one line with no terminator:
//
//	export ANDRONIX_INSTALL_ID=<id>; (curl … get.sh …) && sh $PREFIX/tmp/get.sh install debian --de xfce
//
// Pasted twice before Enter (apps 10.0 and 10.0.1), the first run's last
// argument gets the second copy's "export" glued on, and the second copy's
// id arrives as one more argument: "--de xfceexport ANDRONIX_INSTALL_ID=…".
// unglue takes the known value back and drops the rest; get.sh always runs
// the latest installer, so this fixes it for every app version.

var installIDArg = regexp.MustCompile(`^ANDRONIX_INSTALL_ID=`)

// knownFlags are flags that take no value (what a glued flag can be).
var knownFlags = map[string]bool{"--yes": true, "-y": true, "--no-start": true, "--no-browser": true, "--reinstall": true,
	"--legacy": true, "--plain": true, "--ascii": true, "--no-color": true, "--root": true, "--x11": true, "--modded": true,
	"--purge": true, "--dry-run": true, "--self": true, "--no-self": true}

// unglue returns args without a second paste, and whether one was there.
func unglue(args []string) ([]string, bool) {
	second := false
	for _, a := range args {
		if installIDArg.MatchString(a) {
			second = true
		}
	}
	var out []string
	changed := false
	for i := 0; i < len(args); i++ {
		a := args[i]
		// The second copy's id (and a lone "export" before it when a
		// space got in between): not arguments.
		if installIDArg.MatchString(a) || (a == "export" && i+1 < len(args) && installIDArg.MatchString(args[i+1])) {
			changed = true
			continue
		}
		if base, ok := strings.CutSuffix(a, "export"); ok && base != "" && !known(a) {
			prev := ""
			if i > 0 {
				prev = args[i-1]
			}
			// --de=xfceexport
			if f, v, eq := strings.Cut(base, "="); eq && strings.HasPrefix(f, "--") {
				if knownValue(f, v) || second {
					out = append(out, base)
					changed = true
					continue
				}
			}
			// A value we know, or anything at all once the second id
			// shows it was a double paste (a Modded token, for one).
			if known(base) || knownValue(prev, base) || second {
				out = append(out, base)
				changed = true
				continue
			}
		}
		out = append(out, a)
	}
	return out, changed
}

// known: a flag, distro, desktop or edition the installer knows.
func known(s string) bool {
	if knownFlags[s] {
		return true
	}
	if _, err := conf.ResolveDistro(s); err == nil {
		return true
	}
	if _, _, err := conf.ResolveDesktopCompat(s); err == nil {
		return true
	}
	_, err := conf.ResolveEdition(s)
	return err == nil
}

// knownValue: s is a valid value for the flag before it.
func knownValue(flag, s string) bool {
	switch flag {
	case "--de", "--desktop", "--wm":
		_, _, err := conf.ResolveDesktopCompat(s)
		return err == nil
	case "--edition":
		_, err := conf.ResolveEdition(s)
		return err == nil
	}
	return false
}
