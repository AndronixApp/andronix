package proot

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TmpDir's cleanup must keep every file of a live proot (another session
// of the same distro), whatever its kind, and drop the dead ones.
func TestTmpDirKeepsLiveSessions(t *testing.T) {
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("needs /proc (Linux, Android)")
	}
	home := t.TempDir()
	t.Setenv("ANDRONIX_HOME", home)
	dir := filepath.Join(home, "tmp")
	os.MkdirAll(dir, 0o700)
	live, dead := os.Getpid(), 1<<22+7 // above pid_max
	var keep, drop []string
	for _, kind := range []string{"proot", "prooted", "prootshm"} {
		keep = append(keep, fmt.Sprintf("%s-%d-AbC123", kind, live))
		drop = append(drop, fmt.Sprintf("%s-%d-AbC123", kind, dead))
	}
	drop = append(drop, "stray-file")
	for _, n := range append(append([]string{}, keep...), drop...) {
		os.WriteFile(filepath.Join(dir, n), nil, 0o600)
	}
	if got := TmpDir(); got != dir {
		t.Fatalf("TmpDir() = %s", got)
	}
	for _, n := range keep {
		if _, err := os.Stat(filepath.Join(dir, n)); err != nil {
			t.Errorf("removed a live session's %s", n)
		}
	}
	for _, n := range drop {
		if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
			t.Errorf("kept %s", n)
		}
	}
}

func TestOldSyscallOrder(t *testing.T) {
	for rel, want := range map[string]bool{
		"4.4.302-gcc24b0a8f68a": true, "3.18.140": true, "4.8.0": false,
		"4.14.186-perf": false, "5.10.66-android12": false, "6.6.30": false, "": false,
	} {
		if got := oldSyscallOrder(rel); got != want {
			t.Errorf("oldSyscallOrder(%q) = %v", rel, got)
		}
	}
}

// The env hook may call SetEnv itself (the app's compat code does): no
// deadlock, and its value is used.
func TestCompatEnvHookReentrant(t *testing.T) {
	defer func() { EnvHook, envSet, extra = nil, false, nil }()
	envSet, extra = false, nil
	EnvHook = func() []string { SetEnv([]string{"A=1"}); return []string{"A=1"} }
	done := make(chan []string)
	go func() { done <- compatEnv() }()
	select {
	case env := <-done:
		if len(env) != 1 || env[0] != "A=1" {
			t.Errorf("env %v", env)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("compatEnv deadlocked")
	}
}

func TestPulseServer(t *testing.T) {
	t.Setenv("ANDRONIX_HOST", "")
	if got := PulseServer(); got != "127.0.0.1" {
		t.Errorf("Termux: %q, want 127.0.0.1 (Termux's PulseAudio)", got)
	}
	t.Setenv("ANDRONIX_HOST", "standalone")
	if got := PulseServer(); got != "unix:/tmp/.andronix-pulse/native" {
		t.Errorf("standalone: %q", got)
	}
}

func TestXDGConfigDirsStandalone(t *testing.T) {
	root := t.TempDir()
	tg := &Target{ID: "debian", Rootfs: root}
	t.Setenv("ANDRONIX_HOST", "")
	os.MkdirAll(filepath.Join(root, "etc/xdg/andronix-standalone"), 0o755)
	if x := tg.xdgConfigDirs(); x != "" {
		t.Errorf("Termux: %q, want none", x)
	}
	t.Setenv("ANDRONIX_HOST", "standalone")
	if x := tg.xdgConfigDirs(); x != "/etc/xdg/andronix-standalone:/etc/xdg" {
		t.Errorf("standalone: %q", x)
	}
	os.MkdirAll(filepath.Join(root, "etc/xdg/andronix-light"), 0o755)
	os.MkdirAll(filepath.Join(root, "etc/andronix"), 0o755)
	os.WriteFile(filepath.Join(root, "etc/andronix/profile"), []byte("light\n"), 0o644)
	if x := tg.xdgConfigDirs(); x != "/etc/xdg/andronix-standalone:/etc/xdg/andronix-light:/etc/xdg" {
		t.Errorf("standalone + light: %q", x)
	}
	tg.XDGDefault = "/etc:/etc/xdg:/usr/share" // LXQt (startlxqt's own default)
	if x := tg.xdgConfigDirs(); x != "/etc/xdg/andronix-standalone:/etc/xdg/andronix-light:/etc:/etc/xdg:/usr/share" {
		t.Errorf("standalone + light, LXQt: %q", x)
	}
	tg.XDGDefault = ""
	found := false
	for _, e := range tg.Env() {
		found = found || e == "XDG_CONFIG_DIRS=/etc/xdg/andronix-standalone:/etc/xdg/andronix-light:/etc/xdg"
	}
	if !found {
		t.Error("Env() lacks XDG_CONFIG_DIRS")
	}
}

func TestBindArgStandalone(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANDRONIX_HOME", "/data/app/home")
	tg := &Target{ID: "debian", Rootfs: root, User: "alex", UID: "1000", GID: "1000", Home: "/home/alex"}
	os.MkdirAll(filepath.Join(root, "home/alex"), 0o755)
	for in, want := range map[string]string{
		"/storage/emulated/0/Download/Andronix:~/Android/Download":    "/storage/emulated/0/Download/Andronix:/home/alex/Android/Download",
		"/storage/emulated/10/Documents/Andronix:~/Android/Documents": "/storage/emulated/10/Documents/Andronix:/home/alex/Android/Documents",
		"/storage/emulated/0/Pictures:~/Android/Pictures:ro":          "",
		"/data/app/home/d:/tmp/.andronix-d":                           "/data/app/home/d:/tmp/.andronix-d",
		"/storage/emulated/0:/sdcard":                                 "",
		"/storage/emulated/0/Download:~/dl":                           "",
		"/storage/emulated/0/DCIM/../Android/data:~/x":                "",
		"/etc:/mnt/etc": "",
		"nonsense":      "",
	} {
		got, ok := tg.bindArg(in, true)
		if (want == "") == ok || got != want {
			t.Errorf("%s: %q %v, want %q", in, got, ok, want)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "home/alex/Android/Download")); err != nil {
		t.Error("mount point not made")
	}
	if b, err := os.ReadFile(filepath.Join(root, "home/alex/Android/README.txt")); err != nil || len(b) == 0 {
		t.Error("no README")
	}
	// A socket anywhere (the app's GPU server in its tmp) is fine.
	sock := filepath.Join(t.TempDir(), "v.sock")
	if ln, err := net.Listen("unix", sock); err == nil {
		defer ln.Close()
		if got, ok := tg.bindArg(sock+":/tmp/.virgl_test", true); !ok || got != sock+":/tmp/.virgl_test" {
			t.Errorf("socket: %q %v", got, ok)
		}
	}
	// Termux: unchanged.
	if got, ok := tg.bindArg("/anything:/at/all", false); !ok || got != "/anything:/at/all" {
		t.Errorf("termux: %q %v", got, ok)
	}
}

// proot_version for telemetry: the version from --help's first line only.
func TestVersion(t *testing.T) {
	helpOnce.Do(func() {}) // don't run a real proot
	defer func(h string) { helpText = h }(helpText)
	for help, want := range map[string]string{
		"proot 5.1.107.96: chroot, mount --bind, and binfmt_misc without privilege/setup.\n": "5.1.107.96",
		"\nproot v5.1.107-96\nUsage:\n":         "5.1.107-96",
		"Usage: proot [option] ... [command]\n": "",
		"":                                      "",
	} {
		helpText = help
		if got := Version(); got != want {
			t.Errorf("%q: got %q, want %q", help, got, want)
		}
	}
}

// NoSeccomp (support #312) puts PROOT_NO_SECCOMP=1 in every later proot
// run's environment; a user's own setting wins.
func TestHostEnvNoSeccomp(t *testing.T) {
	defer func() { NoSeccomp = false }()
	has := func() bool {
		for _, e := range hostEnv() {
			if e == "PROOT_NO_SECCOMP=1" {
				return true
			}
		}
		return false
	}
	t.Setenv("PROOT_NO_SECCOMP", "")
	os.Unsetenv("PROOT_NO_SECCOMP")
	before := has()
	NoSeccomp = true
	if !has() {
		t.Fatal("NoSeccomp set, PROOT_NO_SECCOMP=1 missing")
	}
	NoSeccomp = false
	if has() != before {
		t.Error("cleared NoSeccomp still changes the environment")
	}
}
