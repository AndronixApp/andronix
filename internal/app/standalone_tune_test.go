package app

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMinuteClockFormat(t *testing.T) {
	for in, want := range map[string]string{
		"%H:%M:%S":         "%H:%M",
		"%a %d %b %T":      "%a %d %b %H:%M",
		"%X":               "%H:%M",
		"%r":               "%I:%M %p",
		"%I:%M.%S %p":      "%I:%M %p",
		"%a %d %b   %H:%M": "%a %d %b   %H:%M", // the Modded panel: already minutes
		"%R":               "%R",
	} {
		got, changed := minuteClockFormat(in)
		if got != want || changed != (in != want) {
			t.Errorf("%q: %q %v, want %q", in, got, changed, want)
		}
	}
}

const panelWithSecondsClock = `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfce4-panel" version="1.0">
  <property name="panels" type="array">
    <value type="int" value="1"/>
  </property>
  <property name="plugins" type="empty">
    <property name="plugin-1" type="string" value="whiskermenu"/>
    <property name="plugin-3" type="string" value="clock">
      <property name="digital-time-format" type="string" value="%H:%M:%S"/>
      <property name="digital-date-format" type="string" value="%A %d %B"/>
    </property>
    <property name="plugin-8" type="string" value="clock">
      <property name="digital-format" type="string" value="%a %T"/>
    </property>
    <property name="plugin-9" type="string" value="clock"/>
  </property>
</channel>
`

func TestFixClockFormats(t *testing.T) {
	var doc xnode
	if err := xml.Unmarshal([]byte(panelWithSecondsClock), &doc); err != nil {
		t.Fatal(err)
	}
	if n := fixClockFormats(&doc); n != 2 {
		t.Errorf("%d changed", n)
	}
	var buf strings.Builder
	xml.NewEncoder(&buf).Encode(doc)
	out := buf.String()
	for _, want := range []string{`name="digital-time-format" type="string" value="%H:%M"`, `name="digital-format" type="string" value="%a %H:%M"`, `name="digital-date-format" type="string" value="%A %d %B"`, `name="plugin-9" type="string" value="clock"`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s:\n%s", want, out)
		}
	}
	if n := fixClockFormats(&doc); n != 0 {
		t.Errorf("second pass changed %d", n)
	}
}

// A home done by the first release (unnumbered marker, compositing off)
// gets the new entries once; what it already had isn't touched again.
func TestStandaloneHomesUpgrade(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home/alex")
	ch := filepath.Join(home, ".config/xfce4/xfconf/xfce-perchannel-xml")
	os.MkdirAll(ch, 0o755)
	os.MkdirAll(filepath.Join(root, "root"), 0o755)
	// The user turned compositing back on after the first release; that stays.
	os.WriteFile(filepath.Join(ch, "xfwm4.xml"), []byte(`<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfwm4" version="1.0">
  <property name="general" type="empty">
    <property name="use_compositing" type="bool" value="true"/>
    <property name="box_move" type="bool" value="false"/>
  </property>
</channel>
`), 0o644)
	os.WriteFile(filepath.Join(ch, "xfce4-panel.xml"), []byte(panelWithSecondsClock), 0o644)
	marker := filepath.Join(home, standaloneMarker)
	os.MkdirAll(filepath.Dir(marker), 0o755)
	os.WriteFile(marker, []byte("compositing off (standalone app)\n"), 0o644)
	if v := standaloneHomeDone(marker); v != 1 {
		t.Fatalf("old marker read as version %d", v)
	}
	applyStandaloneHomes(root)
	b, _ := os.ReadFile(filepath.Join(ch, "xfwm4.xml"))
	s := string(b)
	if !strings.Contains(s, `name="use_compositing" type="bool" value="true"`) {
		t.Errorf("the user's compositing choice was overwritten:\n%s", s)
	}
	// Box move/resize are layer-only: the home keeps its own (VNC) choice.
	if !strings.Contains(s, `name="box_move" type="bool" value="false"`) || strings.Contains(s, "box_resize") {
		t.Errorf("box move/resize written into the home:\n%s", s)
	}
	b, _ = os.ReadFile(filepath.Join(ch, "xfce4-panel.xml"))
	if !strings.Contains(string(b), `value="%H:%M"`) || strings.Contains(string(b), "%S") {
		t.Errorf("clock not at minutes:\n%s", b)
	}
	if v := standaloneHomeDone(marker); v != standaloneHomeVersion {
		t.Errorf("marker now %d", v)
	}
	// The user wants seconds and opaque moves again: a later start leaves that alone.
	os.WriteFile(filepath.Join(ch, "xfce4-panel.xml"), []byte(panelWithSecondsClock), 0o644)
	applyStandaloneHomes(root)
	if b, _ = os.ReadFile(filepath.Join(ch, "xfce4-panel.xml")); !strings.Contains(string(b), "%H:%M:%S") {
		t.Error("the user's clock format was overwritten")
	}
	// A home with no xfce files gets the marker only.
	if _, err := os.Stat(filepath.Join(root, "root", standaloneMarker)); err != nil {
		t.Error("root: no marker")
	}
	if _, err := os.Stat(filepath.Join(root, "root/.config/xfce4")); err == nil {
		t.Error("root: channel files made up")
	}
	// Every layer entry has a version for homes, except the layer-only ones
	// (a new one without is never applied to old homes).
	layerOnly := map[string]bool{"xfwm4:/general/box_move:bool:false": true, "xfwm4:/general/box_resize:bool:false": true}
	for _, e := range standaloneXfconf {
		if standaloneSince[e] == 0 && !layerOnly[e] {
			t.Errorf("%s has no standaloneSince", e)
		}
	}
}

// The standalone layer turns box_move/box_resize off (xfwm4 grabs the X
// server for wireframe moves, which freezes helper-driven drags), and a distro panel
// default with a seconds clock gets a minute one in the layer.
func TestStandaloneLayerClockAndBoxMove(t *testing.T) {
	root := xfwm4Root(t, xfwm4DefaultsSample)
	writeXfwm4Defaults(root)
	rel := "xfce4/xfconf/xfce-perchannel-xml/xfce4-panel.xml"
	os.MkdirAll(filepath.Join(root, "etc/xdg", filepath.Dir(rel)), 0o755)
	os.WriteFile(filepath.Join(root, "etc/xdg", rel), []byte(panelWithSecondsClock), 0o644)
	os.MkdirAll(filepath.Join(root, filepath.Dir(xfcePanelFile)), 0o755)
	os.WriteFile(filepath.Join(root, xfcePanelFile), []byte(panelWithSecondsClock), 0o644)
	if err := ApplyStandaloneLayer(root); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(filepath.Join(root, "etc/xdg/andronix-standalone/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml"))
	for _, want := range []string{`name="use_compositing" type="bool" value="false"`, `name="box_move" type="bool" value="false"`, `name="box_resize" type="bool" value="false"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("layer lacks %s:\n%s", want, b)
		}
	}
	b, _ = os.ReadFile(filepath.Join(root, "etc/xdg/andronix-standalone", rel))
	if !strings.Contains(string(b), `value="%H:%M"`) || strings.Contains(string(b), "%S") || !strings.Contains(string(b), `name="panels"`) {
		t.Errorf("layer panel channel:\n%s", b)
	}
	b, _ = os.ReadFile(filepath.Join(root, xfcePanelFile))
	if strings.Contains(string(b), "%S") {
		t.Errorf("panel default.xml still has seconds:\n%s", b)
	}
	// Without a panel channel in the distro, the layer has none (the panel would see an empty one).
	root2 := xfwm4Root(t, xfwm4DefaultsSample)
	writeXfwm4Defaults(root2)
	if err := ApplyStandaloneLayer(root2); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root2, "etc/xdg/andronix-standalone", rel)); err == nil {
		t.Error("a panel channel was made up")
	}
}
