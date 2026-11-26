package proot

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/sys"
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

// #319: --kernel-release (and so proot's kompat traps) is kept on old phones
// that need the kernel-version spoof, and dropped once the real kernel clears
// the floor — covering the GKI 6.6/6.12 "android15/16" crash cluster.
func TestSpoofKernelRelease(t *testing.T) {
	for _, c := range []struct {
		rel  string
		want bool
	}{
		{"3.18.140", true},
		{"4.4.302-perf+", true},
		{"4.14.117", true},
		{"5.10.101", true},
		{"5.15.78-android13", true},
		{"6.0.0", false},
		{"6.1.0-android14", false},
		{"6.6.127-android15-8-ge56cf6b09cca-ab15511674-4k", false},
		{"6.12.69-android16-00001-g1234-4k", false},
		{"", true},      // unparseable: keep the spoof
		{"weird", true}, // unparseable: keep the spoof
	} {
		if got := SpoofKernelRelease(c.rel); got != c.want {
			t.Errorf("SpoofKernelRelease(%q) = %v, want %v", c.rel, got, c.want)
		}
	}
}

// Support #5402, #3812: on a 32-bit userland with a real kernel before 5.1,
// uname must not say 5.1 or newer. Go 1.26 picks futex_time64 from uname
// alone there, and the in-distro andronix (and any other Go program) dies
// at start. Everything else keeps the 6.12 stand-in.
func TestSpoofedRelease(t *testing.T) {
	for _, c := range []struct {
		rel  string
		arch sys.Arch
		want string
	}{
		{"4.19.188-perf-00550-g163beabcf523", "arm", "5.0.0-andronix"}, // #5402, Xiaomi 2302EPCC4I
		{"4.14.199-27418755-abA032FXXS9CZA2", "arm", "5.0.0-andronix"}, // #3812, Galaxy A03
		{"3.18.140", "arm", "5.0.0-andronix"},
		{"5.0.21", "arm", "5.0.0-andronix"},
		{"4.19.188", "i686", "5.0.0-andronix"},
		{"5.1.0", "arm", "6.12.0-andronix"}, // futex_time64 arrived in 5.1
		{"5.4.254-android11", "arm", "6.12.0-andronix"},
		{"5.15.78-android13", "i686", "6.12.0-andronix"},
		{"4.19.188", "aarch64", "6.12.0-andronix"}, // 64-bit: no time64 variants to pick
		{"4.4.302-perf+", "x86_64", "6.12.0-andronix"},
		{"", "arm", "6.12.0-andronix"}, // unparseable: the old behaviour
		{"weird", "arm", "6.12.0-andronix"},
	} {
		if got := SpoofedRelease(c.rel, c.arch); got != c.want {
			t.Errorf("SpoofedRelease(%q, %s) = %q, want %q", c.rel, c.arch, got, c.want)
		}
	}
}

// The stand-in for old 32-bit phones must itself stay below 5.1 (or it
// brings the crash back) and keep the -andronix mark reports look for.
func TestSpoofRelease32OldIsBelowTime64(t *testing.T) {
	var major, minor int
	if n, _ := fmt.Sscanf(spoofRelease32Old, "%d.%d", &major, &minor); n < 2 || major > 5 || major == 5 && minor >= 1 {
		t.Errorf("spoofRelease32Old = %q: must parse and be older than 5.1", spoofRelease32Old)
	}
	for _, r := range []string{spoofRelease, spoofRelease32Old} {
		if !strings.HasSuffix(r, "-andronix") {
			t.Errorf("%q must end in -andronix (guestKernel)", r)
		}
	}
}
