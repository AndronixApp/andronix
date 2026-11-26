//go:build network

package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/netx"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
)

// Real network (go test -tags network -run TestAptMirrorsLive -v): every apt
// mirror in DISTRO_MIRROR_FALLBACK answers the probe for the suites our
// images use, on both ARM CPUs. How far each is behind the distro's own
// server is logged, and a mirror that the probe would pass over today (more
// than mirrorMaxLag behind) is named; that part doesn't fail the test, since
// a mirror may be behind on any given day (card #399).
func TestAptMirrorsLive(t *testing.T) {
	suites := map[string][]string{
		"ubuntu24": {"noble noble-updates noble-backports", "noble-security"},
		"ubuntu":   {"resolute resolute-updates resolute-backports", "resolute-security"},
		"debian":   {"trixie trixie-updates", "trixie-security"},
		"kali":     {"kali-rolling"},
	}
	date := func(base, suite string) time.Time {
		b, err := netx.Peek(context.Background(), strings.TrimSuffix(base, "/")+"/dists/"+suite+"/InRelease", 4096)
		if err != nil {
			return time.Time{}
		}
		d, _ := pkgmgr.ReleaseDate(b)
		return d
	}
	for id, stanzas := range suites {
		d, err := conf.LoadDistro(id)
		if err != nil {
			t.Fatal(err)
		}
		groups := d.MirrorFallbacks()
		for g, list := range groups {
			home := list[0]
			for _, u := range list {
				for _, h := range d.MirrorPrimaries() {
					if u == h {
						home = u
					}
				}
			}
			// Debian's second group is the security archive.
			group := stanzas
			if len(groups) > 1 {
				group = stanzas[g : g+1]
			}
			root := t.TempDir()
			src := filepath.Join(root, "etc/apt/sources.list.d/x.sources")
			os.MkdirAll(filepath.Dir(src), 0o755)
			var sb strings.Builder
			for _, s := range group {
				sb.WriteString("Types: deb\nURIs: " + home + "\nSuites: " + s + "\nComponents: main\n\n")
			}
			os.WriteFile(src, []byte(sb.String()), 0o644)
			for _, arch := range []string{"aarch64", "arm"} {
				t.Setenv("ANDRONIX_ARCH", arch)
				for _, to := range list {
					if why := aptMirrorProblem(context.Background(), root, home, to); why != "" {
						t.Errorf("%s %s: %s is not usable: %s", id, arch, to, why)
					}
				}
			}
			for _, s := range strings.Fields(strings.Join(group, " ")) {
				ref := date(home, s)
				for _, to := range list {
					lag := ref.Sub(date(to, s)).Round(time.Minute)
					note := ""
					if lag > mirrorMaxLag {
						note = "  <- would be passed over after a refresh from " + hostOf(home)
					}
					t.Logf("%-9s %-20s %-34s %8s behind%s", id, s, hostOf(to), lag, note)
				}
			}
		}
	}
}
