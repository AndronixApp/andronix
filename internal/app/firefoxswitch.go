package app

import (
	"os"
	"path/filepath"
	"strings"
)

// When andronix update moves a distro from Debian's firefox-esr to
// Mozilla's Firefox (Kali, #183), the user's Firefox and panel should look
// the same afterwards.

// Firefox keys each installation's profile by a hash of its directory.
// These are the two this switch involves (from installs.ini, Kali a16 and
// Docker, Oct 13).
const (
	esrInstallHash     = "3B6073811A6ABF12" // /usr/lib/firefox-esr
	mozillaInstallHash = "4F96D1932A9F858E" // /usr/lib/firefox (Mozilla's .deb)
)

// carryFirefoxProfile points Mozilla's Firefox at each user's Firefox ESR
// profile, so bookmarks, logins and history carry over. Without it Firefox
// makes a new empty profile ("default-release") on its first start, and the
// old one stays on disk unused. Only for users with an ESR profile whose
// Firefox hasn't started yet (no section for its hash): a profile the user
// already has in the new Firefox is left alone. Returns the users changed.
func carryFirefoxProfile(root string) []string {
	var done []string
	homes, _ := filepath.Glob(filepath.Join(root, "home/*"))
	for _, h := range append(homes, filepath.Join(root, "root")) {
		d := filepath.Join(h, ".mozilla/firefox")
		installs, _ := os.ReadFile(filepath.Join(d, "installs.ini"))
		profiles, err := os.ReadFile(filepath.Join(d, "profiles.ini"))
		if err != nil {
			continue
		}
		in, pr := string(installs), string(profiles)
		if iniHas(in, mozillaInstallHash) || iniHas(pr, "Install"+mozillaInstallHash) {
			continue
		}
		p := iniGet(in, esrInstallHash, "Default")
		if p == "" {
			p = iniGet(pr, "Install"+esrInstallHash, "Default")
		}
		if p == "" || !isDir(filepath.Join(d, p)) {
			continue
		}
		sec := func(name string) string { return "[" + name + "]\nDefault=" + p + "\nLocked=1\n\n" }
		if appendINI(filepath.Join(d, "profiles.ini"), pr, sec("Install"+mozillaInstallHash)) != nil {
			continue
		}
		appendINI(filepath.Join(d, "installs.ini"), in, sec(mozillaInstallHash))
		done = append(done, filepath.Base(h))
	}
	return done
}

// iniHas says whether an INI text has a [group].
func iniHas(s, group string) bool {
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) == "["+group+"]" {
			return true
		}
	}
	return false
}

// appendINI adds a section to an INI file whose text is s (a missing file
// is created), keeping a blank line between sections.
func appendINI(p, s, sec string) error {
	if s != "" && !strings.HasSuffix(s, "\n\n") {
		if strings.HasSuffix(s, "\n") {
			s += "\n"
		} else {
			s += "\n\n"
		}
	}
	mode := os.FileMode(0o644)
	if st, err := os.Stat(p); err == nil {
		mode = st.Mode().Perm()
	}
	return os.WriteFile(p, []byte(s+sec), mode)
}

// panelBrowserESR moves XFCE's panel from firefox-esr to firefox once
// firefox-esr's desktop file is gone: the docklike plugin's pinned apps and
// the Whisker menu's favourites (Modded images list only the browser that
// was installed, firefox-esr on Kali), and launcher buttons holding a copy
// of firefox-esr.desktop (which would start the removed browser). System
// defaults in /etc/xdg and each user's saved copies. Returns the files
// changed.
func panelBrowserESR(root string) []string {
	if hasAny(root, "usr/share/applications/firefox-esr.desktop") || !hasAny(root, "usr/share/applications/firefox.desktop") {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(root, "etc/xdg/xfce4/panel/*.rc"))
	for _, h := range []string{"home/*", "root"} {
		f, _ := filepath.Glob(filepath.Join(root, h, ".config/xfce4/panel/*.rc"))
		files = append(files, f...)
	}
	var done []string
	// A launcher keeps its own copy of the desktop file, named in the
	// panel's settings: same name, Firefox's contents.
	ff, err := os.ReadFile(filepath.Join(root, "usr/share/applications/firefox.desktop"))
	for _, h := range []string{"etc/xdg", "home/*/.config", "root/.config"} {
		l, _ := filepath.Glob(filepath.Join(root, h, "xfce4/panel/launcher-*/firefox-esr*.desktop"))
		for _, f := range l {
			st, e := os.Stat(f)
			if err == nil && e == nil && os.WriteFile(f, ff, st.Mode().Perm()) == nil {
				done = append(done, strings.TrimPrefix(f, root))
			}
		}
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		lines := strings.Split(string(b), "\n")
		changed := false
		for i, l := range lines {
			k, v, ok := strings.Cut(l, "=")
			var sep, from, to string
			switch {
			case ok && k == "pinned": // docklike: IDs, ';'
				sep, from, to = ";", "firefox-esr", "firefox"
			case ok && k == "favorites": // Whisker: desktop files, ','
				sep, from, to = ",", "firefox-esr.desktop", "firefox.desktop"
			default:
				continue
			}
			if nv := swapItem(v, sep, from, to); nv != v {
				lines[i], changed = k+"="+nv, true
			}
		}
		if !changed {
			continue
		}
		st, err := os.Stat(f)
		if err != nil || os.WriteFile(f, []byte(strings.Join(lines, "\n")), st.Mode().Perm()) != nil {
			continue
		}
		done = append(done, strings.TrimPrefix(f, root))
	}
	return done
}

// swapItem replaces the item from with to in a sep-separated list, keeping
// its place; when to is already listed, from is dropped instead.
func swapItem(list, sep, from, to string) string {
	items := strings.Split(list, sep)
	has := false
	for _, it := range items {
		if it == to {
			has = true
		}
	}
	var out []string
	for _, it := range items {
		switch {
		case it != from:
			out = append(out, it)
		case !has:
			out = append(out, to)
			has = true
		}
	}
	return strings.Join(out, sep)
}

func isDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}
