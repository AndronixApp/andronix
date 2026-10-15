package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeAll(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for p, s := range files {
		p = filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func readText(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// What Firefox ESR 140 leaves after a first start (Modded Kali, Oct 13).
const esrProfiles = `[Profile1]
Name=default
IsRelative=1
Path=dwq12ayq.default
Default=1

[Profile0]
Name=default-esr
IsRelative=1
Path=do7x7x8y.default-esr

[General]
StartWithLastProfile=1
Version=2

[Install3B6073811A6ABF12]
Default=do7x7x8y.default-esr
Locked=1

`
const esrInstalls = "[3B6073811A6ABF12]\nDefault=do7x7x8y.default-esr\nLocked=1\n\n"

// Mozilla's Firefox opens the ESR profile: both files name it for its own
// install hash (#183).
func TestCarryFirefoxProfile(t *testing.T) {
	root := t.TempDir()
	ff := "home/alex/.mozilla/firefox/"
	writeAll(t, root, map[string]string{
		ff + "profiles.ini": esrProfiles, ff + "installs.ini": esrInstalls,
		ff + "do7x7x8y.default-esr/prefs.js": "",
	})
	if u := carryFirefoxProfile(root); len(u) != 1 || u[0] != "alex" {
		t.Fatalf("changed %v, want [alex]", u)
	}
	pr := readText(t, filepath.Join(root, ff+"profiles.ini"))
	in := readText(t, filepath.Join(root, ff+"installs.ini"))
	if got := iniGet(pr, "Install4F96D1932A9F858E", "Default"); got != "do7x7x8y.default-esr" {
		t.Errorf("profiles.ini: Mozilla's install opens %q\n%s", got, pr)
	}
	if got := iniGet(in, "4F96D1932A9F858E", "Default"); got != "do7x7x8y.default-esr" {
		t.Errorf("installs.ini: Mozilla's install opens %q\n%s", got, in)
	}
	if iniGet(pr, "Install3B6073811A6ABF12", "Default") != "do7x7x8y.default-esr" || !strings.HasPrefix(pr, esrProfiles) {
		t.Errorf("the ESR entries must stay as they were:\n%s", pr)
	}
	if strings.Contains(pr, "\n\n\n") || strings.Contains(in, "\n\n\n") {
		t.Errorf("one blank line between sections:\n%s\n%s", pr, in)
	}
	// A second update changes nothing.
	if u := carryFirefoxProfile(root); len(u) != 0 {
		t.Errorf("second run changed %v", u)
	}
}

// Left alone: a user who already started Mozilla's Firefox (its own
// profile, maybe with new bookmarks), one without an ESR profile, and a
// profile entry whose folder is gone.
func TestCarryFirefoxProfileLeavesAlone(t *testing.T) {
	root := t.TempDir()
	used := esrProfiles + "[Install4F96D1932A9F858E]\nDefault=n4mj1nw7.default-release\nLocked=1\n\n"
	writeAll(t, root, map[string]string{
		"home/used/.mozilla/firefox/profiles.ini":             used,
		"home/used/.mozilla/firefox/installs.ini":             esrInstalls + "[4F96D1932A9F858E]\nDefault=n4mj1nw7.default-release\nLocked=1\n\n",
		"home/used/.mozilla/firefox/do7x7x8y.default-esr/x":   "",
		"home/new/.mozilla/firefox/profiles.ini":              "[General]\nStartWithLastProfile=1\nVersion=2\n",
		"home/gone/.mozilla/firefox/profiles.ini":             esrProfiles,
		"home/gone/.mozilla/firefox/installs.ini":             esrInstalls,
		"home/nofirefox/.config/xfce4/panel/whiskermenu-1.rc": "favorites=firefox-esr.desktop\n",
	})
	if u := carryFirefoxProfile(root); len(u) != 0 {
		t.Errorf("changed %v, want none", u)
	}
	if got := readText(t, filepath.Join(root, "home/used/.mozilla/firefox/profiles.ini")); got != used {
		t.Errorf("used profile rewritten:\n%s", got)
	}
}

// The dock and Whisker favourites name firefox once firefox-esr is gone,
// in /etc/xdg and in users' saved copies; other lines and apps unchanged.
func TestPanelBrowserESR(t *testing.T) {
	root := t.TempDir()
	dock := "[user]\npinned=thunar;xfce4-terminal;firefox-esr;codium;\n"
	whisker := "button-icon=/usr/share/andronix/andronix.svg\nfavorites=firefox-esr.desktop,thunar.desktop,vlc.desktop\nrecent=firefox-esr.desktop\n"
	writeAll(t, root, map[string]string{
		"usr/share/applications/firefox.desktop":                       "[Desktop Entry]\nExec=firefox %u\n",
		"home/alex/.config/xfce4/panel/launcher-3/firefox-esr.desktop": "[Desktop Entry]\nExec=/usr/lib/firefox-esr/firefox-esr %u\n",
		"etc/xdg/xfce4/panel/docklike.rc":                              dock,
		"etc/xdg/xfce4/panel/whiskermenu-1.rc":                         whisker,
		"home/alex/.config/xfce4/panel/docklike-5.rc":                  "pinned=firefox;firefox-esr;thunar;\n",
		"home/alex/.config/xfce4/panel/whiskermenu-1.rc":               whisker,
		"root/.config/xfce4/panel/whiskermenu-1.rc":                    "favorites=thunar.desktop\n",
		"home/alex/.config/xfce4/panel/launcher-9/firefox.desktop":     "",
	})
	got := panelBrowserESR(root)
	if len(got) != 5 {
		t.Errorf("changed %v, want 5 files", got)
	}
	if s := readText(t, filepath.Join(root, "home/alex/.config/xfce4/panel/launcher-3/firefox-esr.desktop")); s != "[Desktop Entry]\nExec=firefox %u\n" {
		t.Errorf("launcher copy still starts firefox-esr:\n%s", s)
	}
	for p, want := range map[string]string{
		"etc/xdg/xfce4/panel/docklike.rc":                "[user]\npinned=thunar;xfce4-terminal;firefox;codium;\n",
		"etc/xdg/xfce4/panel/whiskermenu-1.rc":           strings.Replace(whisker, "favorites=firefox-esr.desktop", "favorites=firefox.desktop", 1),
		"home/alex/.config/xfce4/panel/docklike-5.rc":    "pinned=firefox;thunar;\n", // already there: the ESR entry goes
		"home/alex/.config/xfce4/panel/whiskermenu-1.rc": strings.Replace(whisker, "favorites=firefox-esr.desktop", "favorites=firefox.desktop", 1),
		"root/.config/xfce4/panel/whiskermenu-1.rc":      "favorites=thunar.desktop\n",
	} {
		if s := readText(t, filepath.Join(root, p)); s != want {
			t.Errorf("%s:\n%s\nwant\n%s", p, s, want)
		}
	}
	// firefox-esr still installed (kept by a metapackage): nothing changes.
	root2 := t.TempDir()
	writeAll(t, root2, map[string]string{
		"usr/share/applications/firefox.desktop":     "",
		"usr/share/applications/firefox-esr.desktop": "",
		"etc/xdg/xfce4/panel/docklike.rc":            dock,
	})
	if got := panelBrowserESR(root2); len(got) != 0 {
		t.Errorf("firefox-esr still installed: changed %v", got)
	}
}
