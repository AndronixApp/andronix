package app

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Performance profiles (DESIGN.md section 11). "light" is data from the
// desktop's conf (DE_LIGHT_*), written under /etc/xdg/andronix-light,
// which xstartup puts first in XDG_CONFIG_DIRS so it layers over the
// distro's (and a Modded edition's) defaults without touching them.
// "balanced" removes it. Light is picked automatically below 3 GB of RAM.
const (
	lightDir       = "etc/xdg/andronix-light"
	profileFile    = "etc/andronix/profile"
	AutoLightBelow = 3000 // MB of RAM
)

// Profile returns the install's profile ("balanced" if unset).
func Profile(root string) string {
	b, err := os.ReadFile(filepath.Join(root, profileFile))
	if err != nil {
		return "balanced"
	}
	if p := strings.TrimSpace(string(b)); p == "light" {
		return p
	}
	return "balanced" // also an empty or garbled file
}

// profileSet reports whether the install has a valid profile on record
// (an empty or garbled file, left by a kill mid-write, counts as unset).
func profileSet(root string) bool {
	b, err := os.ReadFile(filepath.Join(root, profileFile))
	p := strings.TrimSpace(string(b))
	return err == nil && (p == "light" || p == "balanced")
}

// ApplyProfile writes (or removes) the light layer for a desktop.
func ApplyProfile(root string, de *conf.Desktop, profile string) error {
	os.MkdirAll(filepath.Join(root, "etc/andronix"), 0o755)
	if err := writeFile(filepath.Join(root, profileFile), profile+"\n"); err != nil {
		return err
	}
	os.RemoveAll(filepath.Join(root, lightDir))
	if profile != "light" || de == nil || de.None() {
		return nil
	}
	base := filepath.Join(root, lightDir)
	for _, n := range de.LightAutostartHide {
		if err := writeFile(filepath.Join(base, "autostart", n+".desktop"),
			"[Desktop Entry]\nType=Application\nName="+n+"\nExec=true\nHidden=true\n"); err != nil {
			return err
		}
	}
	if err := writeINI(root, base, de.LightXDGConfig); err != nil {
		return err
	}
	return writeXfconf(root, base, de.LightXfconf)
}

// standaloneXfconf is the standalone app's layer: no compositing in
// xfwm4 (S24 Ultra 170 -> 200 in glmark2, and smoother dragging).
var standaloneXfconf = []string{"xfwm4:/general/use_compositing:bool:false"}

// ApplyStandaloneLayer writes /etc/xdg/andronix-standalone (used by the
// session when ANDRONIX_HOST=standalone; see xstartup).
func ApplyStandaloneLayer(root string) error {
	base := filepath.Join(root, "etc/xdg/andronix-standalone")
	os.RemoveAll(base)
	if err := writeXfconf(root, base, standaloneXfconf); err != nil {
		return err
	}
	// XFCE's clients start together, without Thunar's daemon (sessionstart.go).
	if err := writeSessionLayer(root, base, true); err != nil {
		return err
	}
	applyStandaloneHomes(root)
	return nil
}

// safeKeys are what display --safe does, as the SAFE line names them:
// software GL, a fresh saved session, and the light profile's settings.
var safeKeys = []string{"swrender", "fresh_session", "light"}

// writeSafeLayer writes /etc/xdg/andronix-safe: the light profile's
// settings (whatever the install's profile) plus the standalone ones, for
// a desktop started in safe mode (xstartup puts it first).
func writeSafeLayer(root string, de *conf.Desktop) error {
	base := filepath.Join(root, "etc/xdg/andronix-safe")
	os.RemoveAll(base)
	if de == nil || de.None() {
		return nil
	}
	for _, n := range de.LightAutostartHide {
		if err := writeFile(filepath.Join(base, "autostart", n+".desktop"),
			"[Desktop Entry]\nType=Application\nName="+n+"\nExec=true\nHidden=true\n"); err != nil {
			return err
		}
	}
	if err := writeINI(root, base, de.LightXDGConfig); err != nil {
		return err
	}
	return writeXfconf(root, base, append(append([]string{}, de.LightXfconf...), standaloneXfconf...))
}

// standaloneMarker records that a home got the standalone defaults.
const standaloneMarker = ".config/andronix/standalone-defaults"

// applyStandaloneHomes puts the standalone layer's settings into each
// user's own xfconf files, once per home: a channel file in ~/.config wins
// over every system layer, and xfconfd writes the whole channel there
// (compositing on included) after a session without the layer, or one in
// Termux before a backup moved here. After this one time, the user's own
// choices stay.
func applyStandaloneHomes(root string) {
	homes := []string{filepath.Join(root, "root")}
	if ents, err := os.ReadDir(filepath.Join(root, "home")); err == nil {
		for _, e := range ents {
			if e.IsDir() {
				homes = append(homes, filepath.Join(root, "home", e.Name()))
			}
		}
	}
	for _, h := range homes {
		if _, err := os.Stat(filepath.Join(h, standaloneMarker)); err == nil {
			continue
		}
		for _, e := range standaloneXfconf {
			f := strings.SplitN(e, ":", 4)
			if len(f) != 4 {
				continue
			}
			setXfconfFile(filepath.Join(h, ".config/xfce4/xfconf/xfce-perchannel-xml", f[0]+".xml"), f[1], f[2], f[3])
		}
		if st, err := os.Stat(h); err == nil {
			p := filepath.Join(h, standaloneMarker)
			os.MkdirAll(filepath.Dir(p), 0o755)
			os.WriteFile(p, []byte("compositing off (standalone app)\n"), 0o644)
			if s, ok := st.Sys().(*syscall.Stat_t); ok {
				os.Lchown(filepath.Dir(p), int(s.Uid), int(s.Gid))
				os.Lchown(p, int(s.Uid), int(s.Gid))
			}
		}
	}
}

// setXfconfFile sets one property in a user's channel file, if the file
// exists (without one, the system layers apply anyway).
func setXfconfFile(p, prop, typ, val string) {
	b, err := os.ReadFile(p)
	if err != nil {
		return
	}
	var doc xnode
	if xml.Unmarshal(b, &doc) != nil || doc.XMLName.Local != "channel" {
		return
	}
	doc.setProp(strings.Split(strings.Trim(prop, "/"), "/"), typ, val)
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if enc.Encode(doc) != nil {
		return
	}
	buf.WriteString("\n")
	st, _ := os.Stat(p)
	tmp := p + ".andronix"
	if os.WriteFile(tmp, buf.Bytes(), 0o644) == nil {
		if st != nil {
			if s, ok := st.Sys().(*syscall.Stat_t); ok {
				os.Lchown(tmp, int(s.Uid), int(s.Gid))
			}
			os.Chmod(tmp, st.Mode().Perm())
		}
		os.Rename(tmp, p)
	}
}

func writeFile(p, s string) error {
	os.MkdirAll(filepath.Dir(p), 0o755)
	return sys.WriteFileAtomic(p, []byte(s), 0o644)
}

// writeINI sets "<file>:<group>:<key>=<value>" entries in base/<file>,
// starting from the distro's /etc/xdg/<file> as writeXfconf does: some
// programs read only the first one found in XDG_CONFIG_DIRS (pcmanfm-qt
// takes the whole first pcmanfm-qt/<profile> dir), so a layer with just
// the changed keys would hide the rest (LXQt's wallpaper and colour, so a
// black desktop).
func writeINI(root, base string, entries []string) error {
	byFile := map[string][][3]string{}
	var order []string
	for _, e := range entries {
		parts := strings.SplitN(e, ":", 3)
		if len(parts) != 3 || !strings.Contains(parts[2], "=") || strings.Contains(parts[0], "..") || strings.HasPrefix(parts[0], "/") {
			continue
		}
		if _, ok := byFile[parts[0]]; !ok {
			order = append(order, parts[0])
		}
		k, v, _ := strings.Cut(parts[2], "=")
		byFile[parts[0]] = append(byFile[parts[0]], [3]string{parts[1], k, v})
	}
	for _, f := range order {
		p := filepath.Join(base, f)
		b, _ := os.ReadFile(filepath.Join(root, "etc/xdg", f))
		s := string(b)
		for _, kv := range byFile[f] {
			s = iniSet(s, kv[0], kv[1], kv[2])
		}
		if err := writeFile(p, s); err != nil {
			return err
		}
	}
	return nil
}

// xfconf XML, kept generic so existing channel files round-trip.
type xnode struct {
	XMLName xml.Name
	Attrs   []xml.Attr `xml:",any,attr"`
	Nodes   []xnode    `xml:",any"`
}

func (n *xnode) attr(k string) string {
	for _, a := range n.Attrs {
		if a.Name.Local == k {
			return a.Value
		}
	}
	return ""
}

func (n *xnode) setAttr(k, v string) {
	for i, a := range n.Attrs {
		if a.Name.Local == k {
			n.Attrs[i].Value = v
			return
		}
	}
	n.Attrs = append(n.Attrs, xml.Attr{Name: xml.Name{Local: k}, Value: v})
}

// setProp sets /a/b/c to (type, value), creating "empty" parents.
func (n *xnode) setProp(path []string, typ, val string) {
	for i := range n.Nodes {
		c := &n.Nodes[i]
		if c.XMLName.Local == "property" && c.attr("name") == path[0] {
			if len(path) == 1 {
				c.setAttr("type", typ)
				c.setAttr("value", val)
				c.Nodes = nil
				return
			}
			c.setProp(path[1:], typ, val)
			return
		}
	}
	c := xnode{XMLName: xml.Name{Local: "property"}, Attrs: []xml.Attr{{Name: xml.Name{Local: "name"}, Value: path[0]}}}
	if len(path) == 1 {
		c.setAttr("type", typ)
		c.setAttr("value", val)
	} else {
		c.setAttr("type", "empty")
		c.setProp(path[1:], typ, val)
	}
	n.Nodes = append(n.Nodes, c)
}

// writeXfconf merges "<channel>:/prop/path:<type>:<value>" entries into a
// copy of the channel's system-wide defaults, under base.
func writeXfconf(root, base string, entries []string) error {
	byCh := map[string][]string{}
	var order []string
	for _, e := range entries {
		f := strings.SplitN(e, ":", 4)
		if len(f) != 4 || !strings.HasPrefix(f[1], "/") || strings.ContainsAny(f[0], "/.") {
			continue
		}
		if _, ok := byCh[f[0]]; !ok {
			order = append(order, f[0])
		}
		byCh[f[0]] = append(byCh[f[0]], e)
	}
	rel := "xfce4/xfconf/xfce-perchannel-xml"
	for _, ch := range order {
		doc := xnode{XMLName: xml.Name{Local: "channel"}, Attrs: []xml.Attr{{Name: xml.Name{Local: "name"}, Value: ch}, {Name: xml.Name{Local: "version"}, Value: "1.0"}}}
		if b, err := os.ReadFile(filepath.Join(root, "etc/xdg", rel, ch+".xml")); err == nil {
			var cur xnode
			if xml.Unmarshal(b, &cur) == nil && cur.XMLName.Local == "channel" {
				doc = cur
			}
		}
		for _, e := range byCh[ch] {
			f := strings.SplitN(e, ":", 4)
			doc.setProp(strings.Split(strings.Trim(f[1], "/"), "/"), f[2], f[3])
		}
		var buf bytes.Buffer
		buf.WriteString(xml.Header)
		enc := xml.NewEncoder(&buf)
		enc.Indent("", "  ")
		if err := enc.Encode(doc); err != nil {
			return err
		}
		buf.WriteString("\n")
		if err := writeFile(filepath.Join(base, rel, ch+".xml"), buf.String()); err != nil {
			return err
		}
	}
	return nil
}

// Tune is `andronix tune <distro> [--profile light|balanced]`.
func Tune(name, profile string) error {
	d, err := resolveDistro(name, "tune")
	if err != nil {
		return err
	}
	in := Open(d)
	if !in.Installed() {
		return ui.Errorf("not_installed", d.Label()+" isn't installed", "There's nothing to tune.", "Install it first: andronix install "+d.ID)
	}
	de, _ := conf.ResolveDesktop(in.Get("DE"))
	ram := sys.TotalRAMMB()
	if profile == "" {
		fmt.Println()
		ui.Row("Profile", Profile(in.Rootfs), 9)
		ui.Row("Desktop", in.Get("DE_NAME"), 9)
		if ram > 0 {
			ui.Row("RAM", fmt.Sprintf("%d MB", ram), 9)
		}
		fmt.Println()
		ui.Note("light: no compositing, fewer background apps, no thumbnails, 16-bit colour and 1280x720 over VNC. Change with: andronix tune " + d.ID + " --profile light|balanced")
		fmt.Println()
		return nil
	}
	if profile != "light" && profile != "balanced" {
		return ui.Errorf("unknown_profile", "Unknown profile '"+profile+"'", "Profiles are light and balanced.", "Try: andronix tune "+d.ID+" --profile light")
	}
	if de == nil || de.None() {
		return ui.Errorf("no_desktop_to_tune", "No desktop to tune", d.Label()+" is installed without a desktop.", "Profiles only change the desktop.")
	}
	if err := ApplyProfile(in.Rootfs, de, profile); err != nil {
		return ui.Errorf("couldn_t_change_the_profile", "Couldn't change the profile", err.Error(), "Run the same command again.")
	}
	writeXstartup(in.Rootfs, de)
	fmt.Println()
	ui.OK(d.Label() + " now uses the " + profile + " profile. It applies the next time you run vncserver-start.")
	fmt.Println()
	return nil
}

// SessionPrep runs inside the desktop session (from xstartup, on its
// D-Bus): applies or resets the desktop's DE_LIGHT_GSETTINGS for the
// current profile. Quiet; never fails the session.
func SessionPrep() error {
	sendTrace("guest-session-prep")
	startAgent() // the Andronix app's launch agent (standalone only)
	b, _ := os.ReadFile("/etc/andronix-release")
	de, err := conf.ResolveDesktop(conf.Parse(b).Get("ANDRONIX_DE"))
	if err != nil || len(de.LightGSettings) == 0 {
		return nil
	}
	if _, err := sys.LookPath("gsettings"); err != nil {
		return nil
	}
	light := Profile("/") == "light"
	for _, e := range de.LightGSettings {
		f := strings.Fields(e)
		if len(f) < 3 {
			continue
		}
		if light {
			sys.Command("gsettings", "set", f[0], f[1], strings.Join(f[2:], " ")).Run()
		} else {
			sys.Command("gsettings", "reset", f[0], f[1]).Run()
		}
	}
	return nil
}
