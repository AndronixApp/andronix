package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

func TestListAppsAndIcons(t *testing.T) {
	root := t.TempDir()
	w := func(rel, s string) {
		p := filepath.Join(root, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(s), 0o644)
	}
	apps := "usr/share/applications/"
	w(apps+"firefox-esr.desktop", "[Desktop Entry]\nType=Application\nName=Firefox ESR\nComment=Web browser\nExec=/usr/lib/firefox-esr/firefox-esr %u\nIcon=firefox-esr\nTerminal=false\nCategories=Network;WebBrowser;;\n[Desktop Action new]\nName=New window\n")
	w(apps+"htop.desktop", "[Desktop Entry]\nType=Application\nName=htop\nExec=htop\nTerminal=true\nIcon=htop\n")
	w(apps+"hidden.desktop", "[Desktop Entry]\nType=Application\nName=Hidden\nExec=x\nNoDisplay=true\n")
	w(apps+"kdeonly.desktop", "[Desktop Entry]\nType=Application\nName=KDE thing\nExec=x\nOnlyShowIn=KDE;\n")
	w(apps+"notxfce.desktop", "[Desktop Entry]\nType=Application\nName=Not XFCE\nExec=x\nNotShowIn=XFCE;GNOME;\n")
	w(apps+"tryexec.desktop", "[Desktop Entry]\nType=Application\nName=Missing\nExec=nothere\nTryExec=nothere\n")
	w(apps+"link.desktop", "[Desktop Entry]\nType=Link\nName=A link\nURL=https://andronix.app\n")
	w(apps+"bad id!.desktop", "[Desktop Entry]\nType=Application\nName=Bad id\nExec=x\n")
	// Firefox: 48 and 256 in hicolor (256 wins: the smallest >= 192);
	// htop: only scalable SVG, through an absolute symlink in the rootfs.
	w("usr/share/icons/hicolor/48x48/apps/firefox-esr.png", "48")
	w("usr/share/icons/hicolor/256x256/apps/firefox-esr.png", "256")
	w("usr/share/real/htop.svg", "<svg/>")
	os.MkdirAll(filepath.Join(root, "usr/share/icons/hicolor/scalable/apps"), 0o755)
	os.Symlink("/usr/share/real/htop.svg", filepath.Join(root, "usr/share/icons/hicolor/scalable/apps/htop.svg"))
	de, _ := conf.ResolveDesktop("xfce")
	got := listApps(root, de)
	var ids []string
	for _, a := range got {
		ids = append(ids, a.ID)
	}
	if want := "firefox-esr htop"; joinSp(ids) != want {
		t.Fatalf("apps %v, want %s", ids, want)
	}
	if strings.Join(got[0].Categories, ",") != "Network,WebBrowser" || got[1].Categories == nil || len(got[1].Categories) != 0 {
		t.Errorf("categories: %q %q", got[0].Categories, got[1].Categories)
	}
	if got[0].Name != "Firefox ESR" || got[0].Comment != "Web browser" || got[0].Terminal || !got[1].Terminal {
		t.Errorf("fields: %+v", got)
	}
	if ic := findIcons(root, "firefox-esr", de); filepath.Base(filepath.Dir(filepath.Dir(ic.big))) != "256x256" || filepath.Base(filepath.Dir(filepath.Dir(ic.small))) != "256x256" {
		t.Errorf("firefox icons %+v, want the 256 PNG", ic)
	}
	if ic := findIcons(root, "htop", de); ic.svg != filepath.Join(root, "usr/share/real/htop.svg") || ic.big != "" {
		t.Errorf("htop icons %+v, want the SVG resolved inside the rootfs", ic)
	}
	// 128 PNG and an SVG (XFCE's own apps): the PNG is the fallback.
	w("usr/share/icons/hicolor/128x128/apps/org.xfce.mousepad.png", "128")
	w("usr/share/icons/hicolor/scalable/apps/org.xfce.mousepad.svg", "<svg/>")
	if ic := findIcons(root, "org.xfce.mousepad", de); ic.big != "" || ic.svg == "" || ic.small == "" {
		t.Errorf("mousepad icons %+v, want svg plus the 128 PNG as fallback", ic)
	}
	if ic := findIcons(root, "nothing", de); ic != (icons{}) {
		t.Errorf("found %+v for a missing icon", ic)
	}
}

func TestIconDirSize(t *testing.T) {
	for n, want := range map[string]int{"128x128": 128, "64x64@2": 128, "scalable": 0, "symbolic": 0, "16x24": 0} {
		if got := iconDirSize(n); got != want {
			t.Errorf("%s: %d, want %d", n, got, want)
		}
	}
}

func joinSp(s []string) string {
	out := ""
	for i, x := range s {
		if i > 0 {
			out += " "
		}
		out += x
	}
	return out
}
