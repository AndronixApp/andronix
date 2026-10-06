package app

import (
	"os"
	"strings"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

// termuxOnly are the commands that manage installs from Termux (or the
// Andronix app), outside any distro. Typed inside a distro they used to
// fail on Termux-only checks ("$PREFIX isn't set", no proot), support #49
// #93; the desktop has its own note (Desktop).
var termuxOnly = map[string]bool{
	"install": true, "i": true, "start": true, "login": true, "run": true,
	"remove": true, "uninstall": true, "rm": true, "update": true, "upgrade": true,
	"list": true, "ls": true, "backup": true, "restore": true, "clean": true,
	"tune": true, "display": true,
	// doctor checks the phone; inside a distro it saw proot's made-up
	// kernel and no Termux.
	"doctor": true,
}

// guestDistro is the distro andronix runs inside, or "" on the host.
// ANDRONIX_DISTRO is in every session's environment; sudo may drop it,
// so /etc/andronix-release is the fallback.
func guestDistro() string {
	if d := os.Getenv("ANDRONIX_DISTRO"); d != "" {
		return d
	}
	if inGuest() {
		return conf.Parse(readFile("/etc/andronix-release")).Get("ANDRONIX_DISTRO")
	}
	return ""
}

// InsideDistro answers a Termux-only command typed inside a distro with
// where to type it instead, and reports whether it did. args are the
// command line after "andronix".
func InsideDistro(cmd string, args []string) bool {
	if !termuxOnly[cmd] {
		return false
	}
	d := guestDistro()
	if d == "" {
		return false
	}
	insideNote(d, "andronix "+strings.Join(args, " "), "andronix "+cmd+" runs in Termux, outside %s. Two steps:")
	return true
}
