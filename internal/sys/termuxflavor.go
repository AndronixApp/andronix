package sys

import (
	"os"
	"path/filepath"
	"strings"
)

// TermuxFlavor is which Termux build runs us: f_droid, github,
// google_play_store, unknown (Termux, but it doesn't say which), or ""
// off Termux. Termux 0.119+ names it TERMUX_APP__APK_RELEASE, older ones
// TERMUX_APK_RELEASE; Play's builds also show it in their version
// (googleplay.*). Telemetry and the compat probe both use this.
func TermuxFlavor() string {
	for _, k := range []string{"TERMUX_APP__APK_RELEASE", "TERMUX_APK_RELEASE"} {
		if r := os.Getenv(k); r != "" {
			return strings.ToLower(r)
		}
	}
	switch v := TermuxVersion(); {
	case strings.HasPrefix(v, "googleplay"):
		return "google_play_store"
	case v != "":
		return "unknown"
	}
	// No Termux session environment (su, SSH, env -i, Tasker): only Play's
	// build has the applib link.
	if IsTermux() {
		if s := AppLibState(); s != "missing" && s != "" {
			return "google_play_store"
		}
		return "unknown"
	}
	return ""
}

// TermuxEnvMissing reports Termux without its session environment (no
// TERMUX_VERSION and friends): andronix started under su, over SSH, by
// env -i or from Tasker.
func TermuxEnvMissing() bool {
	if !IsTermux() {
		return false
	}
	for _, k := range []string{"TERMUX_APP__APK_RELEASE", "TERMUX_APK_RELEASE", "TERMUX_VERSION", "TERMUX_APP__APP_VERSION_NAME"} {
		if os.Getenv(k) != "" {
			return false
		}
	}
	return true
}

// AppLibPath is Play Termux's files/applib, a link to its APK's native
// libraries: Play's proot loads every program through
// applib/libproot-loader.so, the only place it may run programs from
// (support #358). "" off Termux.
func AppLibPath() string {
	if p := Prefix(); p != "" {
		return filepath.Join(filepath.Dir(p), "applib")
	}
	return ""
}

// AppLibState is the applib link's state: link_ok (a link to a folder
// with the loader, outside the app's data), link_bad (a link into the
// app's data, or to no loader), dir (a real folder or file: copied by a
// backup restore, and nothing in it may run), missing, or "" off Termux.
func AppLibState() string {
	p := AppLibPath()
	if p == "" {
		return ""
	}
	fi, err := os.Lstat(p)
	if err != nil {
		return "missing"
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		return "dir"
	}
	t, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "link_bad"
	}
	if _, err := os.Stat(filepath.Join(t, "libproot-loader.so")); err != nil {
		return "link_bad"
	}
	data := filepath.Dir(filepath.Dir(p)) // /data/data/com.termux
	if d, err := filepath.EvalSymlinks(data); err == nil {
		data = d
	}
	if t == data || strings.HasPrefix(t, data+"/") {
		return "link_bad"
	}
	return "link_ok"
}

// TermuxVersion is the Termux app's version (TERMUX_VERSION, or 0.119+'s
// TERMUX_APP__APP_VERSION_NAME), "" off Termux.
func TermuxVersion() string {
	if v := os.Getenv("TERMUX_VERSION"); v != "" {
		return v
	}
	return os.Getenv("TERMUX_APP__APP_VERSION_NAME")
}
