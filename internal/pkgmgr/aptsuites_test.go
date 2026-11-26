package pkgmgr

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

// The sources files as our published images have them (Nov 2026).
const ubuntuSources = `## Ubuntu distribution repository
## Types: Append deb-src to enable the fetching of source package.
Types: deb
URIs: http://ports.ubuntu.com/ubuntu-ports/
Suites: noble noble-updates noble-backports
Components: main universe restricted multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg

## Ubuntu security updates.
Types: deb
URIs: http://ports.ubuntu.com/ubuntu-ports/
Suites: noble-security
Components: main universe restricted multiverse
Signed-By: /usr/share/keyrings/ubuntu-archive-keyring.gpg
`

const debianSources = `Types: deb
# http://snapshot.debian.org/archive/debian/20260918T000000Z
URIs: http://deb.debian.org/debian
Suites: trixie trixie-updates
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp

Types: deb
# http://snapshot.debian.org/archive/debian-security/20260918T000000Z
URIs: http://deb.debian.org/debian-security
Suites: trixie-security
Components: main
Signed-By: /usr/share/keyrings/debian-archive-keyring.pgp
`

func writeRoot(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		p := filepath.Join(root, name)
		os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestAptSuites(t *testing.T) {
	names := func(s []AptSuite) []string {
		var out []string
		for _, x := range s {
			out = append(out, x.Name+"/"+x.Component)
		}
		return out
	}
	root := writeRoot(t, map[string]string{"etc/apt/sources.list.d/ubuntu.sources": ubuntuSources})
	want := []string{"noble/main", "noble-updates/main", "noble-backports/main", "noble-security/main"}
	// With or without the trailing slash; another server's base has none.
	for _, base := range []string{"http://ports.ubuntu.com/ubuntu-ports/", "http://ports.ubuntu.com/ubuntu-ports"} {
		if got := names(AptSuites(root, base)); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v", base, got)
		}
	}
	if got := AptSuites(root, "http://mirrors.ocf.berkeley.edu/ubuntu-ports/"); got != nil {
		t.Errorf("another base: %v", got)
	}

	// Debian: two bases that start the same stay apart.
	root = writeRoot(t, map[string]string{"etc/apt/sources.list.d/debian.sources": debianSources})
	if got := names(AptSuites(root, "http://deb.debian.org/debian")); !reflect.DeepEqual(got, []string{"trixie/main", "trixie-updates/main"}) {
		t.Errorf("debian: %v", got)
	}
	if got := names(AptSuites(root, "http://deb.debian.org/debian-security")); !reflect.DeepEqual(got, []string{"trixie-security/main"}) {
		t.Errorf("debian-security: %v", got)
	}

	// One-line sources: options in brackets, deb-src and comments left out,
	// a flat repository ("./") too; a disabled or deb-src-only stanza as well.
	root = writeRoot(t, map[string]string{
		"etc/apt/sources.list": "# deb http://http.kali.org/kali old main\n" +
			"deb [arch=arm64 signed-by=/usr/share/keyrings/kali.gpg] http://http.kali.org/kali kali-rolling main contrib\n" +
			"deb-src http://http.kali.org/kali kali-src main\n" +
			"deb http://http.kali.org/kali ./\n",
		"etc/apt/sources.list.d/extra.list": "deb\thttp://http.kali.org/kali/ kali-experimental non-free\n",
		"etc/apt/sources.list.d/off.sources": "Types: deb\nURIs: http://http.kali.org/kali\nSuites: kali-off\nComponents: main\nEnabled: no\n\nTypes: deb-src\nURIs: http://http.kali.org/kali\nSuites: kali-source\nComponents: main\n\n" +
			"Types: deb\nURIs: http://http.kali.org/kali\nSuites: kali-false\nComponents: main\nEnabled: False\n\nTypes: deb\nURIs: http://http.kali.org/kali\nSuites: kali-zero\nComponents: main\nEnabled: 0\n",
		"etc/apt/sources.list.d/multi.sources": "Types: deb deb-src\nURIs: http://example.org/x\n http://http.kali.org/kali/\nSuites: kali-last-snapshot\n kali-bleeding-edge\nComponents: contrib\n main\n",
	})
	want = []string{"kali-rolling/main", "kali-experimental/non-free", "kali-last-snapshot/contrib", "kali-bleeding-edge/contrib"}
	if got := names(AptSuites(root, "http://http.kali.org/kali/")); !reflect.DeepEqual(got, want) {
		t.Errorf("one-line and mixed: %v", got)
	}
	if got := AptSuites(t.TempDir(), "http://http.kali.org/kali/"); got != nil {
		t.Errorf("no sources: %v", got)
	}
}

func TestReleaseDate(t *testing.T) {
	head := "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\nOrigin: Ubuntu\nLabel: Ubuntu\nSuite: noble-updates\n"
	for in, want := range map[string]string{
		"Date: Thu, 12 Nov 2026 20:47:45 UTC":   "2026-11-12T20:47:45Z",
		"Date: Sun, 08 Nov 2026  7:49:44 UTC":   "2026-11-08T07:49:44Z", // as mirrors.ocf.berkeley.edu had it
		"Date: Sat, 12 Sep 2026 07:55:41 UTC":   "2026-09-12T07:55:41Z",
		"Date: Thu, 12 Nov 2026 18:05:53 +0000": "2026-11-12T18:05:53Z",
		"Date: Thu, 12 Nov 2026 20:05:53 +0200": "2026-11-12T18:05:53Z",
	} {
		got, ok := ReleaseDate([]byte(head + in + "\nArchitectures: arm64\n"))
		if !ok || got.Format(time.RFC3339) != want {
			t.Errorf("%q: %v %v, want %s", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "<html><title>Sign in</title></html>", head, head + "Date: tomorrow\n", head + "Valid-Until: Thu, 12 Nov 2026 20:47:45 UTC\n"} {
		if got, ok := ReleaseDate([]byte(bad)); ok {
			t.Errorf("%q: got %v", bad, got)
		}
	}
}

func TestAptListDate(t *testing.T) {
	rel := "Origin: Ubuntu\nDate: Tue, 10 Nov 2026 16:40:00 UTC\n"
	root := writeRoot(t, map[string]string{
		"var/lib/apt/lists/ports.ubuntu.com_ubuntu-ports_dists_noble-updates_InRelease":  "-----BEGIN PGP SIGNED MESSAGE-----\nHash: SHA512\n\n" + rel,
		"var/lib/apt/lists/deb.debian.org_debian_dists_trixie_Release":                   rel,
		"var/lib/apt/lists/deb.debian.org_debian-security_dists_trixie-security_Release": "no date here\n",
	})
	// apt %-quotes some characters in a list file's name ("_" is %5f).
	os.WriteFile(filepath.Join(root, "var/lib/apt/lists/example.org_my%5frepo_dists_stable_InRelease"), []byte(rel), 0o644)
	want := time.Date(2026, 11, 10, 16, 40, 0, 0, time.UTC)
	if got := AptListDate(root, "https://example.org/my_repo/", "stable"); !got.Equal(want) {
		t.Errorf("quoted name: %v", got)
	}
	for _, c := range []struct{ base, suite string }{
		{"http://ports.ubuntu.com/ubuntu-ports/", "noble-updates"},
		{"http://ports.ubuntu.com/ubuntu-ports", "noble-updates"},
		{"http://deb.debian.org/debian", "trixie"},
	} {
		if got := AptListDate(root, c.base, c.suite); !got.Equal(want) {
			t.Errorf("%s %s: %v", c.base, c.suite, got)
		}
	}
	// Nothing on disk for it (another mirror's lists, another suite, no date).
	for _, c := range []struct{ base, suite string }{
		{"http://mirrors.ocf.berkeley.edu/ubuntu-ports/", "noble-updates"},
		{"http://ports.ubuntu.com/ubuntu-ports/", "noble"},
		{"http://deb.debian.org/debian-security", "trixie-security"},
	} {
		if got := AptListDate(root, c.base, c.suite); !got.IsZero() {
			t.Errorf("%s %s: %v, want none", c.base, c.suite, got)
		}
	}
}

// A Release file on disk isn't a package index: apt knows a suite's packages
// only with a Packages file for it, whatever its component, CPU and
// compression.
func TestAptHasIndex(t *testing.T) {
	root := writeRoot(t, map[string]string{
		"var/lib/apt/lists/deb.debian.org_debian_dists_trixie_InRelease":                                    "Origin: Debian\n",
		"var/lib/apt/lists/deb.debian.org_debian_dists_trixie-updates_main_binary-arm64_Packages.lz4":       "",
		"var/lib/apt/lists/deb.debian.org_debian-security_dists_trixie-security_main_binary-arm64_Packages": "",
		"var/lib/apt/lists/http.kali.org_kali_dists_kali-rolling_main_binary-arm64_Packages.lz4":            "",
		"var/lib/apt/lists/example.org_my%5frepo_dists_stable_main_binary-all_Packages":                     "",
		"var/lib/apt/lists/ports.ubuntu.com_ubuntu-ports_dists_noble_main_i18n_Translation-en":              "",
	})
	for _, c := range []struct {
		base, suite string
		want        bool
	}{
		{"http://deb.debian.org/debian", "trixie", false}, // the Release file only
		{"http://deb.debian.org/debian", "trixie-updates", true},
		{"http://deb.debian.org/debian-security", "trixie-security", true},
		{"http://deb.debian.org/debian", "trixie-security", false}, // another archive's
		{"http://http.kali.org/kali/", "kali-rolling", true},
		{"http://kali.download/kali/", "kali-rolling", false}, // another mirror's
		{"https://example.org/my_repo/", "stable", true},
		{"http://ports.ubuntu.com/ubuntu-ports/", "noble", false}, // translations only
	} {
		if got := AptHasIndex(root, c.base, c.suite); got != c.want {
			t.Errorf("%s %s: %v, want %v", c.base, c.suite, got, c.want)
		}
	}
	if AptHasIndex(t.TempDir(), "http://deb.debian.org/debian", "trixie") {
		t.Error("no lists folder at all")
	}
}
