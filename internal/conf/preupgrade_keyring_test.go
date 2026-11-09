package conf

import (
	"strings"
	"testing"
)

// TestPacmanKeyringPreUpgrade guards the #1230/#1091 keyring refresh wired into
// DISTRO_PRE_UPGRADE for the pacman distros: it must parse to a real shell
// command (no stray backslash escapes — the conf parser does NOT unescape, so
// a `\$` in the file would ship literally), refresh the keyring package and
// re-populate, and be syntactically valid shell.
func TestPacmanKeyringPreUpgrade(t *testing.T) {
	for _, id := range []string{"arch", "manjaro"} {
		d, err := LoadDistro(id)
		if err != nil {
			t.Fatalf("LoadDistro(%s): %v", id, err)
		}
		cmd := d.PreUpgrade
		if cmd == "" {
			t.Fatalf("%s: DISTRO_PRE_UPGRADE is empty", id)
		}
		if strings.Contains(cmd, `\$`) || strings.Contains(cmd, `\"`) {
			t.Errorf("%s: PreUpgrade has literal backslash escapes (conf parser doesn't unescape): %q", id, cmd)
		}
		for _, want := range []string{"pacman -Sy", "pacman-key --populate", "--refresh-keys", "-keyring"} {
			if !strings.Contains(cmd, want) {
				t.Errorf("%s: PreUpgrade missing %q", id, want)
			}
		}
		// Balanced quotes and braces — a rough guard that the conf value wasn't
		// mangled (bash -n would be stronger but exec is barred in this repo).
		if strings.Count(cmd, `"`)%2 != 0 {
			t.Errorf("%s: PreUpgrade has unbalanced double quotes: %q", id, cmd)
		}
		if strings.Count(cmd, "{") != strings.Count(cmd, "}") {
			t.Errorf("%s: PreUpgrade has unbalanced braces: %q", id, cmd)
		}
	}
}

// TestManjaroArmKeyring: Manjaro ARM signs with its own keyring package too
// (manjaro-arm-keyring); the probe list must include it (#283).
func TestManjaroArmKeyring(t *testing.T) {
	d, err := LoadDistro("manjaro")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d.PreUpgrade, "manjaro-arm-keyring") {
		t.Errorf("manjaro PreUpgrade doesn't refresh manjaro-arm-keyring: %q", d.PreUpgrade)
	}
}
