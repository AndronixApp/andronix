package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

func TestStandaloneHomesOnce(t *testing.T) {
	root := t.TempDir()
	rel := ".config/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml"
	p := filepath.Join(root, "home/alex", rel)
	os.MkdirAll(filepath.Dir(p), 0o755)
	on := `<?xml version="1.0" encoding="UTF-8"?>
<channel name="xfwm4" version="1.0">
  <property name="general" type="empty">
    <property name="use_compositing" type="bool" value="true"/>
    <property name="theme" type="string" value="Default"/>
  </property>
</channel>
`
	os.WriteFile(p, []byte(on), 0o644)
	os.MkdirAll(filepath.Join(root, "root"), 0o755)
	applyStandaloneHomes(root)
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), `name="use_compositing" type="bool" value="false"`) || !strings.Contains(string(b), `name="theme"`) {
		t.Fatalf("not set, or other settings lost:\n%s", b)
	}
	if _, err := os.Stat(filepath.Join(root, "home/alex", standaloneMarker)); err != nil {
		t.Fatal("no marker")
	}
	// The user turns it back on: a later start leaves that alone.
	os.WriteFile(p, []byte(on), 0o644)
	applyStandaloneHomes(root)
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), `value="true"`) {
		t.Fatal("the user's own choice was overwritten")
	}
	// root had no xfwm4.xml: nothing written but the marker.
	if _, err := os.Stat(filepath.Join(root, "root", rel)); err == nil {
		t.Fatal("made a channel file for root")
	}
}

func TestSafeLayer(t *testing.T) {
	root := t.TempDir()
	de, err := conf.ResolveDesktop("xfce")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSafeLayer(root, de); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, "etc/xdg/andronix-safe/xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name="use_compositing" type="bool" value="false"`, `name="box_move" type="bool" value="true"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("safe layer lacks %s:\n%s", want, b)
		}
	}
	if strings.Join(safeKeys, ",") != "swrender,fresh_session,light" {
		t.Errorf("SAFE keys changed: %v (the app maps them)", safeKeys)
	}
}
