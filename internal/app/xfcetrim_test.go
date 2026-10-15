package app

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

// On Debian 13's own files: Thunar's daemon leaves both failsafe sessions
// (the rest renumbered, Count lowered), the status tray leaves the panel,
// and nothing else changes.
func TestTrimXfceDefaults(t *testing.T) {
	root := t.TempDir()
	put := func(rel, fixture string) {
		b, err := os.ReadFile(filepath.Join("testdata/xfce", fixture))
		if err != nil {
			t.Fatal(err)
		}
		os.MkdirAll(filepath.Join(root, filepath.Dir(rel)), 0o755)
		os.WriteFile(filepath.Join(root, rel), b, 0o644)
	}
	put(xfceSessionFile, "xfce4-session.xml")
	put(xfcePanelFile, "panel-default.xml")
	de := &conf.Desktop{SessionDrop: []string{"Thunar"}, PanelDrop: []string{"systray"}}
	if did := trimXfceDefaults(root, de); did != "xfce4-session.xml: 2 dropped, default.xml: 1 dropped" {
		t.Errorf("did: %q", did)
	}
	sb, _ := os.ReadFile(filepath.Join(root, xfceSessionFile))
	s := string(sb)
	if strings.Contains(s, "Thunar") || strings.Contains(s, "Client4_") {
		t.Errorf("Thunar or Client4 left:\n%s", s)
	}
	for _, want := range []string{
		`<property name="Count" type="int" value="4"/>`, // Failsafe: 5 -> 4
		`<property name="Count" type="int" value="3"/>`, // FailsafeWayland: 4 -> 3
		`<property name="Client3_Command" type="array">
        <value type="string" value="xfdesktop"/>`,
		`<property name="Client3_Priority" type="int" value="35"/>`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q", want)
		}
	}
	pb, _ := os.ReadFile(filepath.Join(root, xfcePanelFile))
	p := string(pb)
	if strings.Contains(p, "systray") || strings.Contains(p, `value="6"/>`) {
		t.Errorf("systray left")
	}
	for _, want := range []string{`value="actions"`, `<value type="int" value="5"/>`, `<value type="int" value="7"/>`, `<value type="int" value="1"/>`, `<value type="int" value="2"/>`} {
		if !strings.Contains(p, want) {
			t.Errorf("panel lost %q", want)
		}
	}
	for _, b := range [][]byte{sb, pb} {
		d := xml.NewDecoder(strings.NewReader(string(b)))
		for {
			if _, err := d.Token(); err != nil {
				if err.Error() != "EOF" {
					t.Errorf("not valid XML any more: %v", err)
				}
				break
			}
		}
	}
	// Again: nothing more to do.
	if did := trimXfceDefaults(root, de); did != "" {
		t.Errorf("second run: %q", did)
	}
}
