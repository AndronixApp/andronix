package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

const xfwm4DefaultsSample = `activate_action=bring
borderless_maximize=true
button_layout=O|SHMC
double_click_distance=5
shadow_delta_y=-3
theme=Default
title_font=Sans Bold 9
title_shadow_active=false
use_compositing=true
some_ratio=0.5
# a comment
`

func xfwm4Root(t *testing.T, defaults string) string {
	root := t.TempDir()
	p := filepath.Join(root, xfwm4DefaultsFile)
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte(defaults), 0o644)
	return root
}

func readSys(t *testing.T, root string) string {
	b, err := os.ReadFile(filepath.Join(root, xfwm4SystemFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestXfwm4DefaultsSystemChannel(t *testing.T) {
	root := xfwm4Root(t, xfwm4DefaultsSample)
	if got := writeXfwm4Defaults(root); got != "generated 10 keys" {
		t.Fatalf("status %q", got)
	}
	out := readSys(t, root)
	for _, want := range []string{
		`name="activate_action" type="string" value="bring"`,
		`name="borderless_maximize" type="bool" value="true"`,
		`name="button_layout" type="string" value="O|SHMC"`,
		`name="double_click_distance" type="int" value="5"`,
		`name="shadow_delta_y" type="int" value="-3"`,
		`name="title_font" type="string" value="Sans Bold 9"`,
		`name="title_shadow_active" type="string" value="false"`,
		`name="use_compositing" type="bool" value="true"`,
		`name="some_ratio" type="double" value="0.5"`,
		`<channel name="xfwm4"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "comment") {
		t.Errorf("a comment became a setting:\n%s", out)
	}
	if got := writeXfwm4Defaults(root); got != "up to date" {
		t.Errorf("second run: %q", got)
	}
}

func TestXfwm4DefaultsRegeneratedAfterAnUpgrade(t *testing.T) {
	root := xfwm4Root(t, xfwm4DefaultsSample)
	writeXfwm4Defaults(root)
	// xfwm4 upgraded: a key gone, one new.
	os.WriteFile(filepath.Join(root, xfwm4DefaultsFile), []byte("theme=Default\nnew_key=7\n"), 0o644)
	if got := writeXfwm4Defaults(root); got != "generated 2 keys" {
		t.Fatalf("status %q", got)
	}
	out := readSys(t, root)
	if !strings.Contains(out, `name="new_key" type="int" value="7"`) || strings.Contains(out, "button_layout") {
		t.Errorf("not regenerated from the new defaults:\n%s", out)
	}
}

func TestXfwm4DefaultsKeepTheDistrosOwn(t *testing.T) {
	root := xfwm4Root(t, xfwm4DefaultsSample)
	p := filepath.Join(root, xfwm4SystemFile)
	os.MkdirAll(filepath.Dir(p), 0o755)
	own := `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + `<channel name="xfwm4" version="1.0"><property name="general" type="empty"><property name="theme" type="string" value="Greybird"/></property></channel>` + "\n"
	os.WriteFile(p, []byte(own), 0o644)
	if got := writeXfwm4Defaults(root); got != "distro's own kept" {
		t.Errorf("status %q", got)
	}
	if b, _ := os.ReadFile(p); string(b) != own {
		t.Errorf("the distro's own channel changed:\n%s", b)
	}
}

func TestXfwm4DefaultsWithoutXfwm4(t *testing.T) {
	root := t.TempDir()
	if got := writeXfwm4Defaults(root); got != "no xfwm4" {
		t.Errorf("status %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, xfwm4SystemFile)); err == nil {
		t.Fatal("wrote an xfwm4 channel without xfwm4")
	}
}

// The light layer's xfwm4 channel starts from the generated defaults
// (setupDesktop writes them before applying the profile).
func TestXfwm4DefaultsInLightLayer(t *testing.T) {
	root := xfwm4Root(t, xfwm4DefaultsSample)
	writeXfwm4Defaults(root)
	de, _ := conf.ResolveDesktop("xfce")
	if err := ApplyProfile(root, de, "light"); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(root, lightDir, "xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`name="activate_action" type="string" value="bring"`, `name="use_compositing" type="bool" value="false"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("missing %s in:\n%s", want, b)
		}
	}
}
