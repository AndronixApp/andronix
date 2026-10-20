package sys

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTermuxFlavor(t *testing.T) {
	for _, c := range []struct{ app, old, ver, ver119, want string }{
		{"F_DROID", "", "0.119.0-beta.3", "", "f_droid"},
		{"", "GITHUB", "0.118.3", "", "github"},
		{"", "", "googleplay.2025.01.18", "", "google_play_store"},
		{"", "", "", "googleplay.2026.02.11", "google_play_store"},
		{"", "", "0.118.0", "", "unknown"},
		{"", "", "", "", ""},
	} {
		t.Setenv("TERMUX_APP__APK_RELEASE", c.app)
		t.Setenv("TERMUX_APK_RELEASE", c.old)
		t.Setenv("TERMUX_VERSION", c.ver)
		t.Setenv("TERMUX_APP__APP_VERSION_NAME", c.ver119)
		if got := TermuxFlavor(); got != c.want {
			t.Errorf("%+v: %q, want %q", c, got, c.want)
		}
	}
}

// Play Termux's applib link (support #358), and the flavor without
// Termux's session environment.
func TestAppLibState(t *testing.T) {
	for _, k := range []string{"TERMUX_APP__APK_RELEASE", "TERMUX_APK_RELEASE", "TERMUX_VERSION", "TERMUX_APP__APP_VERSION_NAME", "ANDRONIX_HOST", "ANDRONIX_DISTRO"} {
		t.Setenv(k, "")
	}
	root := t.TempDir()
	data := filepath.Join(root, "data", "com.termux")
	files := filepath.Join(data, "files")
	os.MkdirAll(filepath.Join(files, "usr"), 0o755)
	t.Setenv("PREFIX", filepath.Join(files, "usr"))
	applib := filepath.Join(files, "applib")
	apk := filepath.Join(root, "app", "com.termux-1", "lib", "arm64")
	os.MkdirAll(apk, 0o755)
	os.WriteFile(filepath.Join(apk, "libproot-loader.so"), []byte("x"), 0o755)

	check := func(name, wantState, wantFlavor string) {
		t.Helper()
		if got := AppLibState(); got != wantState {
			t.Errorf("%s: AppLibState = %q, want %q", name, got, wantState)
		}
		if got := TermuxFlavor(); got != wantFlavor {
			t.Errorf("%s: TermuxFlavor = %q, want %q", name, got, wantFlavor)
		}
	}
	check("F-Droid Termux without its environment", "missing", "unknown")
	if !TermuxEnvMissing() {
		t.Error("no TERMUX_* variables: want TermuxEnvMissing")
	}

	os.Symlink(apk, applib)
	check("Play, link to the APK's libraries", "link_ok", "google_play_store")

	os.Remove(applib)
	inData := filepath.Join(files, "copied-libs")
	os.MkdirAll(inData, 0o755)
	os.WriteFile(filepath.Join(inData, "libproot-loader.so"), []byte("x"), 0o755)
	os.Symlink(inData, applib)
	check("Play, link into the app's data", "link_bad", "google_play_store")

	os.Remove(applib)
	os.Symlink(filepath.Join(root, "app", "com.termux-0", "lib", "arm64"), applib)
	check("Play, link to an old APK (gone)", "link_bad", "google_play_store")

	os.Remove(applib)
	os.MkdirAll(applib, 0o755)
	os.WriteFile(filepath.Join(applib, "libproot-loader.so"), []byte("x"), 0o755)
	check("Play, applib restored as a folder", "dir", "google_play_store")

	t.Setenv("TERMUX_VERSION", "googleplay.2026.10.09")
	if TermuxEnvMissing() {
		t.Error("TERMUX_VERSION set: want not missing")
	}
	t.Setenv("PREFIX", "")
	t.Setenv("TERMUX_VERSION", "")
	if _, err := os.Stat("/data/data/com.termux/files/usr"); err != nil && AppLibState() != "" {
		t.Error("off Termux: want AppLibState empty")
	}
}
