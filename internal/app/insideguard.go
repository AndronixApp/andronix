package app

import (
	"fmt"
	"os"
	"strings"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// termuxOnly are the commands that manage installs from Termux (or the
// Andronix app), outside any distro. Typed inside a distro they used to
// fail on Termux-only checks ("$PREFIX isn't set", no proot), support #49
// #93. The desktop and its Termux:X11 options too (support #351: in the
// app's terminal, which is always inside the distro, 'andronix desktop'
// fell through to a Termux:X11 error); not vnc, which runs inside.
var termuxOnly = map[string]bool{
	"install": true, "i": true, "start": true, "login": true, "run": true,
	"remove": true, "uninstall": true, "rm": true, "update": true, "upgrade": true,
	"list": true, "ls": true, "backup": true, "restore": true, "clean": true,
	"tune": true, "display": true, "desktop": true, "x11": true,
	// doctor checks the phone; inside a distro it saw proot's made-up
	// kernel and no Termux.
	"doctor": true,
}

// InApp reports whether this andronix runs for the Andronix app: its
// in-app runtime on the phone, or inside one of the app's distros. Its
// messages then point to the app, not to Termux (owner, 10.1).
func InApp() bool {
	if sys.Standalone() {
		return true
	}
	return guestDistro() != "" && standaloneHost()
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
	// The standalone display's own step inside the distro (DisplayHost
	// runs "andronix display start" there): not a user typing it.
	if cmd == "display" && len(args) > 1 && args[1] == "start" {
		return false
	}
	d := guestDistro()
	if d == "" {
		return false
	}
	line := "andronix " + strings.Join(args, " ")
	if cmd == "desktop" || cmd == "x11" {
		insideDistroNote(d, line)
		return true
	}
	if standaloneHost() && (cmd == "update" || cmd == "upgrade") {
		// The app has no Update action: say what does update (lead, 2.1.1).
		fmt.Println()
		fmt.Print(ui.Box(ui.BoxBrand, "You're inside "+distroName(d), standaloneUpdateLines(d, os.Geteuid() == 0)...))
		fmt.Println()
		return true
	}
	if standaloneHost() {
		// The Andronix app (no Termux): its screens do these.
		insideNoteSteps(d, "andronix "+cmd+" is done from the Andronix app, outside %s. Two steps:",
			"2. Then do it from the app's screens.")
		return true
	}
	insideNote(d, line, "andronix "+cmd+" runs in Termux, outside %s. Two steps:")
	return true
}

// upgradeCmd is how a family's packages are brought up to date, typed in
// the distro.
var upgradeCmd = map[string]string{
	"apt":    "apt update && apt full-upgrade",
	"dnf":    "dnf upgrade",
	"pacman": "pacman -Syu",
	"apk":    "apk upgrade -U",
	"xbps":   "xbps-install -Su",
}

// standaloneUpdateLines answers "andronix update" inside a distro of the
// Andronix app: the Linux runtime comes with the app (Google Play), and
// the distro's packages update with its own package manager, here.
func standaloneUpdateLines(distro string, root bool) []string {
	name, fam := distroName(distro), ""
	if d, err := conf.ResolveDistro(distro); err == nil {
		fam = d.Family
	}
	lines := []string{"andronix itself (the Linux runtime) updates with the Andronix app, from Google Play."}
	if c, ok := upgradeCmd[fam]; ok {
		how := name + "'s packages update here, with its package manager:"
		switch {
		case root:
		case distro == "manjaro": // no sudo under proot (STATUS): as root
			how = name + "'s packages update with its package manager, as root (" + name + " has no sudo here):"
		default:
			c = "sudo " + strings.ReplaceAll(c, "&& ", "&& sudo ")
		}
		lines = append(lines, "", how, "  "+c)
	}
	return lines
}

// distroName is a distro's label ("Debian 13"), or its id.
func distroName(distro string) string {
	if d, err := conf.ResolveDistro(distro); err == nil {
		return d.Label()
	}
	return distro
}
