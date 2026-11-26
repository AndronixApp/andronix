// Package pkgmgr knows each package-manager family: the commands that run
// inside the distro, and how to read progress from their output.
package pkgmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// Family is one package manager (apt, dnf, pacman, apk, xbps).
type Family struct {
	ID string
	// Shell commands run inside the distro as root.
	Update, Upgrade, Clean string
	Install                func(pkgs []string) string
	// Simulate prints one line matching SimLine per package that would
	// change; used to size the progress bar up front.
	SimInstall func(pkgs []string) string
	SimUpgrade string
	SimLine    *regexp.Regexp
	// Progress lines: either Step (count matches; Phases lines per
	// package) or Counter (capture groups "cur" and "total").
	Step    *regexp.Regexp
	Phases  int
	Counter *regexp.Regexp
	// VNC server packages and the group that gets sudo.
	VNC        []string
	AdminGroup string
	// AddUser is the command that creates a login user.
	AddUser func(name, shell string) string
	// Configure writes host-side tweaks into a fresh rootfs.
	Configure func(root string) error
	// PostInstall runs after every package transaction (may be "").
	PostInstall string
	// Repair runs after each successful Update (may be ""): it puts right
	// what a package run that was killed half-way left behind, before the
	// next transaction trips over it.
	Repair string
	// Has is a shell test that succeeds when package %s is available;
	// Installed, when it is installed.
	Has, Installed string
	// Remove takes packages out, with the dependencies nothing else needs.
	Remove func(pkgs []string) string
	// FchmodatShim installs the fchmodat2 preload shim before the first
	// package run (Void: glibc 2.39+ vs proot on Linux 6.6+).
	FchmodatShim bool
}

// Parse returns (cur, total, ok) for a Counter-style line.
func (f *Family) Parse(line string) (int, int, bool) {
	if f.Counter == nil {
		return 0, 0, false
	}
	m := f.Counter.FindStringSubmatch(line)
	if m == nil {
		return 0, 0, false
	}
	cur, _ := strconv.Atoi(m[f.Counter.SubexpIndex("cur")])
	tot, _ := strconv.Atoi(m[f.Counter.SubexpIndex("total")])
	return cur, tot, tot > 0
}

func join(p []string) string { return strings.Join(p, " ") }

func write(root, rel, content string, mode os.FileMode) error {
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	os.Remove(p)
	return os.WriteFile(p, []byte(content), mode)
}

func useradd(group string) func(string, string) string {
	return func(name, shell string) string {
		return fmt.Sprintf("useradd -m -s %s -G %s %s", shell, group, name)
	}
}

// pacman prints plain lines when stdout isn't a terminal; keep it so.
const pac = "pacman --noconfirm --noprogressbar --color never "

// pacmanKeyring makes this phone's pacman keyring unless it's already
// usable. "Usable" is a key pacman trusts fully (validity f: a packager or
// build key signed through the local master key), not "pubring.gpg
// exists": an install stopped during pacman-key --init/--populate (the
// user cancelled, Android closed the app) leaves a pubring.gpg with keys
// nothing trusts, and every later run then skipped --populate and failed
// each package with "invalid or corrupted package (PGP signature)"
// (support #5349). Both pacman-key steps are safe to run again on a
// half-made keyring: --init only makes a master key when there is none,
// --populate re-imports and re-signs. No keyserver is involved.
//
// One fully trusted key isn't proof of a finished --populate either: it
// signs the keys one at a time, and a run killed half-way leaves some
// trusted and some not (review of #3; on x86_64 trust takes several master
// keys plus ownertrust). So the keyring also needs the stamp written once
// --populate has exited 0 (PacmanKeyringStamp). Keyrings made before the
// stamp existed get one more --populate, offline, on their next update.
//
// An install from before the stamp has a finished keyring and no stamp, and
// on a phone where gpg-agent won't start (support #119) that --populate
// fails. So with a trusted key and only the stamp missing, a failed
// --populate doesn't stop the update: the sync goes on with the keys the
// install has, as 2.1.5 did, and the next update tries again (the stamp is
// only written by a --populate that exited 0). With no trusted key a failed
// keyring still stops it, and a half-signed one that --populate can finish
// is finished as before.
//
// gpg is only asked about a keyring that has the stamp (as before) or a
// trustdb.gpg (pacman-key --init makes it, after pubring.gpg): asked about
// an empty directory (the retry code's keyringReset leaves one), gpg would
// create pubring.kbx there, and the keys pacman-key then makes would go
// into it instead of pubring.gpg.
//
// A gpg killed mid-write also leaves its lock files (pubring.gpg.lock,
// .#lk*). gpg only drops a lock when it can tell the owner is gone; when it
// can't (the pid is in use again, which on Android reads as "alive" for any
// other app's process) it waits for good. So with no gpg program running,
// the locks go first (.l2s.: what proot's --link2symlink keeps of them).
const pacmanKeyring = `{ G=/etc/pacman.d/gnupg; b=; for c in /proc/[0-9]*/comm; do read -r n 2>/dev/null <"$c" && case $n in gpg|gpg-agent|gpgsm|dirmngr|keyboxd|pacman-key) b=1 ;; esac; done; ` +
	`[ -n "$b" ] || rm -f "$G"/*.lock "$G"/.#lk* "$G"/.l2s..#lk*; ` +
	`t=; { [ -e "$G"/` + keyringStampName + ` ] || [ -e "$G"/trustdb.gpg ]; } && ` + PacmanKeyringTrusted + ` && t=1; ` +
	`{ [ -n "$t" ] && [ -e "$G"/` + keyringStampName + ` ]; } || ` + PacmanKeyringRebuild +
	` || { [ -n "$t" ] && echo "` + keyringKeptNote + `"; }; }`

// PacmanKeyringTrusted is a shell test (G is the keyring's directory): gpg
// lists a key pacman trusts fully.
const PacmanKeyringTrusted = `gpg --homedir "$G" --batch --no-permission-warning --list-keys --with-colons 2>/dev/null | grep -q '^pub:f:'`

// keyringKeptNote is printed when --populate failed on a keyring that
// already has trusted keys. It must not read as a failure to Diagnose, and
// has no quote characters: it sits inside a shell string.
const keyringKeptNote = "The package signing keys were not set up again; going on with the keys this install already trusts."

// keyringStampName is the file in /etc/pacman.d/gnupg that says --populate
// finished; PacmanKeyringStamp is its full path.
const (
	keyringStampName   = ".andronix-keyring-ok"
	PacmanKeyringStamp = "/etc/pacman.d/gnupg/" + keyringStampName
)

// PacmanKeyringRebuild runs both pacman-key steps whatever the keyring
// looks like (a package still failed its signature check), and stamps the
// keyring finished only when both exit 0.
const PacmanKeyringRebuild = "{ rm -f " + PacmanKeyringStamp + "; pacman-key --init && pacman-key --populate && touch " + PacmanKeyringStamp + "; }"

// PacmanRepair reinstalls packages a killed pacman left half-installed.
// pacman removes the old version's database entry, makes the new one's
// directory, unpacks the files and only then writes desc and files: killed
// in between, the entry has neither. The next run counts the package as
// installed with half its files missing, and any transaction that touches
// it stops with "could not fully load metadata for package ... failed to
// prepare transaction (invalid or corrupted package)" (support #5412).
// The names go to a list first, so a reinstall that fails (no network) is
// tried again next time; names the repos no longer have are dropped.
// A pacman running (the user's own, in another session) may be mid-unpack
// right now, so then nothing is touched (review of #3).
// --overwrite: the half-unpacked files belong to no package. No `exit`:
// the retry code wraps commands (keyringAgentWrap) and runs on after them.
const PacmanRepair = `L=/var/lib/pacman/local; T=/var/lib/pacman/andronix-reinstall
b=; for c in /proc/[0-9]*/comm; do read -r n 2>/dev/null <"$c" && [ "$n" = pacman ] && b=1; done
[ -n "$b" ] && echo "pacman is running; not touching its database"
[ -n "$b" ] || for d in "$L"/*/; do
  [ -d "$d" ] || continue
  [ -s "$d/desc" ] && [ -e "$d/files" ] && continue
  n=${d%/}; n=${n##*/}; n=${n%-*}; n=${n%-*}
  echo "$n" >>"$T" && rm -rf "$d"
done
if [ -z "$b" ] && [ -s "$T" ]; then
  p=; for n in $(sort -u "$T"); do pacman -Si "$n" >/dev/null 2>&1 && p="$p $n"; done
  if [ -n "$p" ]; then
    echo "Reinstalling half-installed packages:$p"
    ` + pac + `-S --overwrite '*' $p && rm -f "$T"
  else
    rm -f "$T"
  fi
fi`

// pacmanMajor reads the installed pacman's major version from its local db.
func pacmanMajor(root string) int {
	m, _ := filepath.Glob(filepath.Join(root, "var/lib/pacman/local/pacman-[0-9]*"))
	if len(m) == 0 {
		return 0
	}
	v := strings.TrimPrefix(filepath.Base(m[0]), "pacman-")
	n, _ := strconv.Atoi(strings.SplitN(v, ".", 2)[0])
	return n
}

// SetMirror points pacman at DISTRO_MIRROR_<arch> (Manjaro).
func SetMirror(root, family, url string) error {
	if url == "" || family != "pacman" {
		return nil
	}
	return write(root, "etc/pacman.d/mirrorlist", "# Andronix: DISTRO_MIRROR\nServer = "+url+"\n", 0o644)
}

const aptEnv = "DEBIAN_FRONTEND=noninteractive APT_LISTCHANGES_FRONTEND=none "

// APT::Keep-Downloaded-Packages=false: apt-get drops the .debs once a run
// has installed them (as the apt command does), so a phone with little
// space doesn't keep them until our clean (support #158).
const aptOpts = "-o Dpkg::Use-Pty=0 -o APT::Color=0 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -o Acquire::Retries=3 -o APT::Keep-Downloaded-Packages=false "

var families = map[string]*Family{
	// Debian, Ubuntu, Kali.
	"apt": {
		ID: "apt",
		// policy, not show: show succeeds for a package with no
		// installable version (and for some virtual ones).
		Has:       "apt-cache policy %s 2>/dev/null | grep -q 'Candidate: [^(]'",
		Installed: "dpkg-query -W -f='${Status}' %s 2>/dev/null | grep -q 'ok installed'",
		Remove: func(p []string) string {
			return aptEnv + "apt-get " + aptOpts + "-y remove --autoremove " + join(p)
		},
		// Images like debian:13 and kali-rolling have no CA store; install
		// it straight away so https sources work (port-a finding).
		Update: aptEnv + "apt-get " + aptOpts + "update && { dpkg -s ca-certificates >/dev/null 2>&1 || " +
			aptEnv + "apt-get " + aptOpts + "-y --no-install-recommends install ca-certificates; }",
		Upgrade: aptEnv + "apt-get " + aptOpts + "-y full-upgrade",
		Clean:   "apt-get clean",
		Install: func(p []string) string {
			return aptEnv + "apt-get " + aptOpts + "-y --no-install-recommends install " + join(p)
		},
		SimInstall: func(p []string) string {
			return aptEnv + "apt-get " + aptOpts + "-s --no-install-recommends install " + join(p)
		},
		SimUpgrade: aptEnv + "apt-get " + aptOpts + "-s full-upgrade",
		SimLine:    regexp.MustCompile(`^Inst `),
		Step:       regexp.MustCompile(`^(Get:\d|Unpacking |Setting up )`),
		Phases:     3,
		VNC:        []string{"tigervnc-standalone-server", "tigervnc-tools"},
		AdminGroup: "sudo",
		AddUser:    useradd("sudo"),
		Configure: func(root string) error {
			return write(root, "etc/apt/apt.conf.d/90andronix",
				"// Andronix: proot can't switch to the _apt user; phone networks drop.\n"+
					"APT::Sandbox::User \"root\";\nAcquire::Retries \"3\";\n", 0o644)
		},
	},
	// Fedora (dnf5).
	"dnf": {
		ID:        "dnf",
		Has:       "dnf -q repoquery --available %s 2>/dev/null | grep -q .",
		Installed: "rpm -q %s >/dev/null 2>&1",
		Remove:    func(p []string) string { return "dnf -y remove " + join(p) },
		Update:    "dnf -y makecache",
		Upgrade:   "dnf -y upgrade",
		Clean:     "dnf clean all",
		Install:   func(p []string) string { return "dnf -y install --setopt=install_weak_deps=False " + join(p) },
		SimInstall: func(p []string) string {
			return "dnf -y install --assumeno --setopt=install_weak_deps=False " + join(p) + " 2>&1 | grep -E '^ [a-zA-Z0-9]' ; true"
		},
		SimUpgrade: "dnf -y upgrade --assumeno 2>&1 | grep -E '^ [a-zA-Z0-9]'; true",
		SimLine:    regexp.MustCompile(`^ \S+\s+\S+\s+\S+`),
		// dnf5's [n/N] counter restarts between download and transaction,
		// so count its lines against the simulated total instead.
		Step:   regexp.MustCompile(`^\[ *[0-9]+/[0-9]+\] `),
		Phases: 2,
		VNC:    []string{"tigervnc-server"},
		// rpm under proot loses sudo's setuid bit (Fedora ships it 0711).
		PostInstall: "[ -f /usr/bin/sudo ] && chmod 4755 /usr/bin/sudo; true",
		AdminGroup:  "wheel",
		AddUser:     useradd("wheel"),
		Configure: func(root string) error {
			return write(root, "etc/dnf/libdnf5.conf.d/90-andronix.conf",
				"# Andronix: fewer retries stall on phone networks\n[main]\nretries=5\nmax_parallel_downloads=5\n", 0o644)
		},
	},
	// Arch Linux (ARM), Manjaro. Findings: docs/ports/arch.md, manjaro.md.
	"pacman": {
		ID:        "pacman",
		Has:       "{ pacman -Si %s || pacman -Sg %s; } >/dev/null 2>&1",
		Installed: "pacman -Qq %s >/dev/null 2>&1",
		Remove:    func(p []string) string { return pac + "-Rns " + join(p) },
		// A fresh rootfs has no keyring (and Manjaro's shared one is
		// deleted in Configure): make this phone's own before the sync.
		Update:     pacmanKeyring + " && " + pac + "-Syy",
		Repair:     PacmanRepair,
		Upgrade:    pac + "-Su",
		Clean:      "rm -f /var/cache/pacman/pkg/*",
		Install:    func(p []string) string { return pac + "-S --needed " + join(p) },
		SimInstall: func(p []string) string { return pac + "-Sp --needed --print-format %n " + join(p) },
		SimUpgrade: "pacman -Qu",
		SimLine:    regexp.MustCompile(`^[a-z0-9@._+-]+( |$)`),
		// " sudo-1.9.17.p2-6-aarch64 downloading..." then "installing sudo...".
		Step:       regexp.MustCompile(`^ .*-(any|aarch64|armv7h|x86_64) downloading|^(installing|upgrading|reinstalling) `),
		Phases:     2,
		VNC:        []string{"tigervnc", "xorg-xauth"},
		AdminGroup: "wheel",
		AddUser:    useradd("wheel"),
		Configure: func(root string) error {
			p := filepath.Join(root, "etc/pacman.conf")
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			s := string(b)
			// Android can hide /proc/mounts entries, which CheckSpace reads.
			s = strings.Replace(s, "\nCheckSpace", "\n#CheckSpace", 1)
			// pacman 7's download sandbox uses Landlock, which Android's app
			// seccomp policy may not allow. pacman 6 warns about the key.
			if pacmanMajor(root) >= 7 && !strings.Contains(s, "\nDisableSandbox") {
				s = strings.Replace(s, "[options]\n", "[options]\nDisableSandbox\n", 1)
			}
			// manjarolinux/base ships an initialised keyring with a master key
			// that every user would share. Update makes a fresh one.
			os.RemoveAll(filepath.Join(root, "etc/pacman.d/gnupg"))
			return write(root, "etc/pacman.conf", s, 0o644)
		},
	},
	// Alpine (apk-tools 3). Findings: docs/ports/alpine.md.
	"apk": {
		ID:         "apk",
		Has:        "apk search -x %s 2>/dev/null | grep -q .",
		Installed:  "apk info -e %s >/dev/null 2>&1",
		Remove:     func(p []string) string { return "apk --no-progress del " + join(p) },
		Update:     "apk --no-progress update",
		Upgrade:    "apk --no-progress upgrade --available",
		Clean:      "rm -rf /var/cache/apk/*",
		Install:    func(p []string) string { return "apk --no-progress add " + join(p) },
		SimInstall: func(p []string) string { return "apk --no-progress add --simulate " + join(p) },
		SimUpgrade: "apk --no-progress upgrade --available --simulate",
		SimLine:    regexp.MustCompile(`^\(\d+/\d+\) `),
		Counter:    regexp.MustCompile(`^\(\s*(?P<cur>\d+)/(?P<total>\d+)\) `),
		VNC:        []string{"tigervnc", "xauth"},
		AdminGroup: "wheel",
		// The base packages include shadow, so useradd exists.
		AddUser: func(name, shell string) string {
			return fmt.Sprintf("useradd -m -s %s -G wheel %s || { adduser -D -s %s %s && addgroup %s wheel; }", shell, name, shell, name, name)
		},
		// apk 3 runs package scripts from a memfd, which proot can't exec,
		// so triggers silently do nothing (black XFCE screen). With
		// memfd_noexec >= 2 apk writes them to /lib/apk/exec instead;
		// proot binds this stand-in over /proc/sys/vm/memfd_noexec.
		Configure: func(root string) error {
			d := filepath.Join(filepath.Dir(root), "sysdata")
			os.MkdirAll(d, 0o755)
			return os.WriteFile(filepath.Join(d, "memfd_noexec"), []byte("2\n"), 0o644)
		},
	},
	// Void (glibc). Findings: docs/ports/void.md.
	"xbps": {
		ID:        "xbps",
		Has:       "xbps-query -R %s >/dev/null 2>&1",
		Installed: "xbps-query %s >/dev/null 2>&1",
		Remove:    func(p []string) string { return "xbps-remove -Ry " + join(p) },
		// xbps refuses other updates until xbps itself is current.
		Update:       "xbps-install -S && xbps-install -yu xbps",
		Upgrade:      "xbps-install -yu",
		Clean:        "xbps-remove -yO",
		Install:      func(p []string) string { return "xbps-install -y " + join(p) },
		SimInstall:   func(p []string) string { return "xbps-install -n " + join(p) },
		SimUpgrade:   "xbps-install -un",
		SimLine:      regexp.MustCompile(` (install|update) `),
		Step:         regexp.MustCompile(`^[^ ]+: (verifying RSA signature|unpacking |configuring )`),
		Phases:       3,
		VNC:          []string{"tigervnc", "xauth"},
		AdminGroup:   "wheel",
		AddUser:      useradd("wheel"),
		Configure:    func(root string) error { return nil },
		FchmodatShim: true,
	},
}

// FilterAvailable is a shell command printing which of pkgs exist.
func (f *Family) FilterAvailable(pkgs []string) string {
	if f.Has == "" || len(pkgs) == 0 {
		return "true"
	}
	return "for p in " + join(pkgs) + "; do " + strings.ReplaceAll(f.Has, "%s", "\"$p\"") + " && echo \"$p\"; done; true"
}

// FilterInstalled is a shell command printing which of pkgs are installed.
func (f *Family) FilterInstalled(pkgs []string) string {
	if len(pkgs) == 0 {
		return "true"
	}
	return "for p in " + join(pkgs) + "; do " + strings.ReplaceAll(f.Installed, "%s", "\"$p\"") + " && echo \"$p\"; done; true"
}

// Get returns a family by id.
func Get(id string) (*Family, error) {
	f, ok := families[id]
	if !ok {
		return nil, fmt.Errorf("unknown package family %q", id)
	}
	return f, nil
}

// RewriteSources applies DISTRO_MIRROR_REWRITE to every package-source
// file the families use.
func RewriteSources(root, from, to string) error {
	if from == "" {
		return nil
	}
	var files []string
	for _, g := range []string{"etc/apt/sources.list", "etc/apt/sources.list.d/*.list", "etc/apt/sources.list.d/*.sources",
		"etc/yum.repos.d/*.repo", "etc/pacman.d/mirrorlist", "etc/apk/repositories", "etc/xbps.d/*.conf", "usr/share/xbps.d/*.conf"} {
		m, _ := filepath.Glob(filepath.Join(root, g))
		files = append(files, m...)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil || !strings.Contains(string(b), from) {
			continue
		}
		if err := write(root, strings.TrimPrefix(f, root+"/"), strings.ReplaceAll(string(b), from, to), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// RewriteBase points the sources from one base URL to another. Unlike
// RewriteSources it matches whole bases: http://deb.debian.org/debian
// leaves http://deb.debian.org/debian-security alone.
func RewriteBase(root, from, to string) error {
	if from == "" || from == to {
		return nil
	}
	var files []string
	for _, g := range []string{"etc/apt/sources.list", "etc/apt/sources.list.d/*.list", "etc/apt/sources.list.d/*.sources",
		"etc/yum.repos.d/*.repo", "etc/pacman.d/mirrorlist", "etc/apk/repositories", "etc/xbps.d/*.conf", "usr/share/xbps.d/*.conf"} {
		m, _ := filepath.Glob(filepath.Join(root, g))
		files = append(files, m...)
	}
	found := false
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		out, n := replaceBase(string(b), from, to)
		if n == 0 {
			continue
		}
		found = true
		if err := write(root, strings.TrimPrefix(f, root+"/"), out, 0o644); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("no source uses %s", from)
	}
	return nil
}

// SourcesUse reports whether any package source uses base.
func SourcesUse(root, base string) bool {
	for _, g := range []string{"etc/apt/sources.list", "etc/apt/sources.list.d/*.list", "etc/apt/sources.list.d/*.sources",
		"etc/yum.repos.d/*.repo", "etc/pacman.d/mirrorlist", "etc/apk/repositories", "etc/xbps.d/*.conf", "usr/share/xbps.d/*.conf"} {
		m, _ := filepath.Glob(filepath.Join(root, g))
		for _, f := range m {
			if b, err := os.ReadFile(f); err == nil {
				if _, n := replaceBase(string(b), base, ""); n > 0 {
					return true
				}
			}
		}
	}
	return false
}

// replaceBase replaces from where it isn't followed by a letter, digit or
// '-' (the start of a longer path segment).
func replaceBase(s, from, to string) (string, int) {
	var b strings.Builder
	n := 0
	for {
		i := strings.Index(s, from)
		if i < 0 {
			b.WriteString(s)
			return b.String(), n
		}
		end := i + len(from)
		next := byte(' ')
		if end < len(s) {
			next = s[end]
		}
		b.WriteString(s[:i])
		if strings.HasSuffix(from, "/") || !(next == '-' || next >= 'a' && next <= 'z' || next >= 'A' && next <= 'Z' || next >= '0' && next <= '9') {
			b.WriteString(to)
			n++
		} else {
			b.WriteString(from)
		}
		s = s[end:]
	}
}

// WriteFile writes a config file into the rootfs.
func WriteFile(root, rel, content string) error { return write(root, rel, content, 0o644) }

// NoSnap pins snapd away (Ubuntu): snaps can't run under proot.
func NoSnap(root string) error {
	return write(root, "etc/apt/preferences.d/no-snap",
		"# Andronix: snaps can't run under proot.\nPackage: snapd\nPin: release a=*\nPin-Priority: -10\n", 0o644)
}

// MozillaRepo adds Mozilla's APT repository (for a real firefox .deb on
// Ubuntu) with a pin that prefers it over Ubuntu's snap wrapper.
func MozillaRepo(root string, key []byte) error {
	if err := write(root, "etc/apt/keyrings/packages.mozilla.org.asc", string(key), 0o644); err != nil {
		return err
	}
	if err := write(root, "etc/apt/sources.list.d/mozilla.sources",
		"Types: deb\nURIs: https://packages.mozilla.org/apt\nSuites: mozilla\nComponents: main\n"+
			"Signed-By: /etc/apt/keyrings/packages.mozilla.org.asc\n", 0o644); err != nil {
		return err
	}
	return write(root, "etc/apt/preferences.d/mozilla",
		"Package: *\nPin: origin packages.mozilla.org\nPin-Priority: 1000\n", 0o644)
}

// MozillaKeyURL is Mozilla's APT signing key.
const MozillaKeyURL = "https://packages.mozilla.org/apt/repo-signing-key.gpg"
