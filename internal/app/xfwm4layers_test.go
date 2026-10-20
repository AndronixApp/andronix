package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

// The standalone, light and safe layers copy the system channel: they carry
// xfwm4's defaults plus their own overrides (keep it that way).
func TestLayersCarryXfwm4Defaults(t *testing.T) {
	root := xfwm4Root(t, xfwm4DefaultsSample)
	writeXfwm4Defaults(root)
	if err := ApplyStandaloneLayer(root); err != nil {
		t.Fatal(err)
	}
	ch := "xfce4/xfconf/xfce-perchannel-xml/xfwm4.xml"
	b, _ := os.ReadFile(filepath.Join(root, "etc/xdg/andronix-standalone", ch))
	if out := string(b); !strings.Contains(out, `name="use_compositing" type="bool" value="false"`) || !strings.Contains(out, `name="theme" type="string" value="Default"`) {
		t.Errorf("standalone layer:\n%s", out)
	}
	xfce, err := conf.ResolveDesktop("xfce")
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSafeLayer(root, xfce); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(filepath.Join(root, "etc/xdg/andronix-safe", ch))
	if out := string(b); !strings.Contains(out, `name="theme" type="string" value="Default"`) || !strings.Contains(out, `name="use_compositing" type="bool" value="false"`) {
		t.Errorf("safe layer:\n%s", out)
	}
}
