package app

import (
	"os"
	"strings"
	"testing"
)

// The step details the app translates (card #174): their keys, and the
// English text the app matches for installer 2.1.0, which must never change
// without telling ui first.
func TestDetailKeysFrozen(t *testing.T) {
	src := map[string]string{}
	for _, f := range []string{"install.go", "update.go", "pack.go", "desktop.go", "manage.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src[f] = string(b)
	}
	for _, c := range []struct{ file, call string }{
		{"install.go", `ui.DetailOf(r, "source.image", "ready-made image, "+ui.Bytes(img.size)`},
		{"install.go", `ui.DetailOf(r, "source.local", "local file"`},
		{"install.go", `ui.DetailOf(r, "source.mirror", "Andronix mirror, "+ui.Bytes(s.size)`},
		{"install.go", `ui.DetailOf(r, "source.upstream", d.Name+"'s own download, "+ui.Bytes(s.size), map[string]any{"distro": d.Name, "distro_id": d.ID, "bytes": s.size}`},
		{"install.go", `ui.DetailOf(r, "source.official", "official image, "+ui.Bytes(s.image.Total)`},
		{"install.go", `ui.DetailOf(r, "source.edition", editionName(edition), map[string]any{"edition": edition}`},
		{"install.go", `ui.DetailOf(r, "verify.image", "ready-made image, sha256 checked"`},
		{"install.go", `ui.DetailOf(r, "verify.sha256", "sha256 checked"`},
		{"install.go", `ui.DetailOf(r, "verify.digests", "digests checked"`},
		{"install.go", `ui.DetailOf(r, "arch", arch.Label(), map[string]any{"arch": string(arch)}`},
		{"install.go", `ui.DetailOf(r, "arch.low_ram", fmt.Sprintf("%s · only %d MB RAM", arch.Label(), ram), map[string]any{"arch": string(arch), "ram_mb": ram}`},
		{"install.go", `ui.DetailOf(r, "packages", packages(n), map[string]any{"count": n}`},
		{"update.go", `ui.DetailOf(r, "update.installed", "new version installed"`},
		{"update.go", `ui.DetailOf(r, "update.version", cur+" → "+m.Latest, map[string]any{"from": cur, "to": m.Latest}`},
		{"desktop.go", `ui.DetailOf(r, "desktop.light", fmt.Sprintf("light profile (%d MB RAM)", ram), map[string]any{"ram_mb": ram}`},
		{"manage.go", `ui.DetailOf(r, "links.repaired", fmt.Sprintf("%d links repaired", k), map[string]any{"count": k}`},
		{"pack.go", `ui.DetailOf(r, "packages", fmt.Sprintf("%d packages", len(fresh)), map[string]any{"count": len(fresh)}`},
	} {
		if !strings.Contains(src[c.file], c.call) {
			t.Errorf("%s: changed or gone (tell ui before changing a detail's text, key or args): %s", c.file, c.call)
		}
	}
}
