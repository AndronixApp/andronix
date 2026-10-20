package app

import (
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/conf"
)

// downloadLabel names the download step: the distro for a free install, the
// edition (distro + desktop + Classic/Modded) for a paid one (#286).
func TestDownloadLabel(t *testing.T) {
	ubuntu := &conf.Distro{Name: "Ubuntu", Version: "26.04"}
	kde := &conf.Desktop{ID: "kde", Name: "KDE Plasma"}
	none := &conf.Desktop{ID: "none", Name: "No desktop"}

	cases := []struct {
		name          string
		d             *conf.Distro
		de            *conf.Desktop
		edition, want string
	}{
		{"free install shows the distro", ubuntu, kde, "", "Downloading Ubuntu 26.04"},
		{"free install, no desktop", ubuntu, none, "", "Downloading Ubuntu 26.04"},
		{"classic edition", ubuntu, kde, "legacy-ubuntu-kde", "Downloading Ubuntu KDE Plasma (Classic)"},
		{"modded edition", ubuntu, kde, "ubuntu-kde", "Downloading Ubuntu KDE Plasma (Modded)"},
	}
	for _, c := range cases {
		if got := downloadLabel(c.d, c.de, c.edition); got != c.want {
			t.Errorf("%s: downloadLabel = %q, want %q", c.name, got, c.want)
		}
	}
}
