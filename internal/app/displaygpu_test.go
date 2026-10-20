package app

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/proot"
)

func TestParseDisplayGPU(t *testing.T) {
	for _, c := range []struct {
		mode, socket, want string
		ok                 bool
	}{
		{"", "", "", true},
		{"off", "/x/.virgl_test", "", true},
		{"virgl", "/data/user/0/app/files/tmp/.virgl_test", "virgl", true},
		{"virgl", "", "", false},
		{"virgl", "relative/.virgl_test", "", false},
		{"virgl", "/with space/s", "", false},
		{"virgl", "/" + string(make([]byte, 120)), "", false},
		{"turnip", "/x", "", false},
	} {
		g, err := parseDisplayGPU(c.mode, c.socket)
		if (err == nil) != c.ok || g.Mode != c.want {
			t.Errorf("parseDisplayGPU(%q, %q) = %+v, %v", c.mode, c.socket, g, err)
		}
	}
}

func TestDisplayGPUBindAndEnv(t *testing.T) {
	g := displayGPU{Mode: "virgl", Socket: "/a/tmp/.virgl_test"}
	if g.bind() != "/a/tmp/.virgl_test:/tmp/.virgl_test" {
		t.Errorf("bind = %q", g.bind())
	}
	if g.guestEnv() != "GALLIUM_DRIVER=virpipe MESA_GL_VERSION_OVERRIDE=4.0 " {
		t.Errorf("guestEnv = %q", g.guestEnv())
	}
	if (displayGPU{}).name() != "off" || g.name() != "virgl" {
		t.Error("name")
	}
}

func TestDisplayGPUUp(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s")
	g := displayGPU{Mode: "virgl", Socket: sock}
	if g.up() {
		t.Fatal("up without a server")
	}
	if t0 := time.Now(); g.waitUp(300*time.Millisecond) || time.Since(t0) < 300*time.Millisecond {
		t.Fatal("waitUp without a server")
	}
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no unix sockets here:", err)
	}
	defer l.Close()
	go func() {
		if c, err := l.Accept(); err == nil {
			c.Close()
		}
	}()
	if !g.up() {
		t.Error("server listening but not up")
	}
	if !g.waitUp(time.Second) {
		t.Error("waitUp with a server")
	}
	if (displayGPU{Socket: sock}).up() {
		t.Error("off is never up")
	}
	os.Remove(sock)
}

func TestDisplayGPUVerdict(t *testing.T) {
	for _, c := range []struct{ out, status, reason string }{
		{"GL_RENDERER   = virgl (Adreno (TM) 750)\nGL_VERSION = 4.0", "ok", ""},
		{"GL_RENDERER   = llvmpipe (LLVM 15.0.6, 128 bits)", "fallback", "renderer-llvmpipe"},
		{"Error: couldn't open display :1", "fallback", "no-renderer"},
		{"NO_GLXGEARS\n", "ok", "unchecked"},
	} {
		s, r := displayGPUVerdict(c.out)
		if s != c.status || r != c.reason {
			t.Errorf("displayGPUVerdict(%q) = %s %s, want %s %s", c.out, s, r, c.status, c.reason)
		}
	}
}

func TestApplyDisplayGPU(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ANDRONIX_HOME", home)
	t.Setenv("ANDRONIX_BINDS", "")
	sock := filepath.Join(home, "s")
	g := displayGPU{Mode: "virgl", Socket: sock}

	// No GPU display: sessions are unchanged.
	tg := &proot.Target{}
	applyDisplayGPU(tg, "debian")
	if len(tg.ExtraEnv) != 0 || os.Getenv("ANDRONIX_BINDS") != "" {
		t.Fatal("changed without a GPU display")
	}

	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("no unix sockets here:", err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	g.save("debian")

	// Another distro's sessions: unchanged.
	applyDisplayGPU(tg, "ubuntu")
	if len(tg.ExtraEnv) != 0 {
		t.Error("another distro got the GPU")
	}
	// The same distro: the socket and Mesa's environment, the bind once.
	applyDisplayGPU(tg, "debian")
	applyDisplayGPU(tg, "debian")
	if tg.ExtraEnv["GALLIUM_DRIVER"] != "virpipe" || tg.ExtraEnv["MESA_GL_VERSION_OVERRIDE"] != "4.0" {
		t.Errorf("env %v", tg.ExtraEnv)
	}
	if b := os.Getenv("ANDRONIX_BINDS"); b != g.bind() {
		t.Errorf("binds %q", b)
	}
	// The display ended: unchanged again.
	displayGPUClear()
	t2 := &proot.Target{}
	applyDisplayGPU(t2, "debian")
	if len(t2.ExtraEnv) != 0 {
		t.Error("GPU after the display ended")
	}
}
