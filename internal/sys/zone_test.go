package sys

import (
	"os"
	"path/filepath"
	"testing"
)

// support #443 (Asia/Calcutta from Android, Ubuntu 26.04 without
// tzdata-legacy) and #435 (Africa/Lusaka).
func TestResolveZone(t *testing.T) {
	root := t.TempDir()
	zi := filepath.Join(root, "usr/share/zoneinfo")
	for _, z := range []string{"Asia/Kolkata", "Europe/Berlin", "Africa/Maputo", "Europe/Kyiv", "Etc/UTC"} {
		os.MkdirAll(filepath.Join(zi, filepath.Dir(z)), 0o755)
		os.WriteFile(filepath.Join(zi, z), []byte("TZif"), 0o644)
	}
	os.WriteFile(filepath.Join(zi, "tzdata.zi"), []byte("Z Asia/Kolkata 5:53:28 - LMT 1854\nL Europe/Kyiv Europe/Kiev\nL Europe/Kiev Europe/Zaporozhye\n"), 0o644)
	for in, want := range map[string]string{
		"Europe/Berlin":     "Europe/Berlin",
		" Europe/Berlin\r":  "Europe/Berlin",
		"europe/berlin":     "Europe/Berlin",
		"Asia/Calcutta":     "Asia/Kolkata", // legacyZones (not in this tzdata.zi)
		"Africa/Lusaka":     "Africa/Maputo",
		"Europe/Kiev":       "Europe/Kyiv", // tzdata.zi
		"Europe/Zaporozhye": "Europe/Kyiv", // two hops
		"asia/calcutta":     "",            // no case folding through links
		"Mars/Olympus":      "",
		"Asia":              "", // a folder isn't a zone
		"../etc/passwd":     "",
		"":                  "",
	} {
		if got := ResolveZone(root, in); got != want {
			t.Errorf("ResolveZone(%q) = %q, want %q", in, got, want)
		}
	}
}
