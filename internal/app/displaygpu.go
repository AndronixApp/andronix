package app

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// GPU acceleration for the standalone app's display (`display <distro>
// --gpu virgl --gpu-socket <path>`). The app runs the VirGL server
// (jniLibs libvirgl.so: virglrenderer drawing with the phone's GLES) and
// passes its socket; Mesa in the distro sends GL to it (virpipe, the vtest
// client), which works with Xvfb: frames come back by readback, as with
// llvmpipe. Without --gpu, or with --gpu off, nothing changes.

// displayGPUGuestSocket is where Mesa's vtest client looks for the server.
const displayGPUGuestSocket = "/tmp/.virgl_test"

// displayGPU is the GPU choice of one display start.
type displayGPU struct {
	Mode   string // "virgl" or "" (off)
	Socket string // the server's socket on the host
}

// parseDisplayGPU checks the flags: off unless --gpu virgl, which needs
// --gpu-socket. turnip and auto are the Termux path's modes (feat-gpu);
// the app only runs VirGL for now.
func parseDisplayGPU(mode, socket string) (displayGPU, error) {
	switch mode {
	case "", "off":
		return displayGPU{}, nil
	case "virgl":
		if socket == "" || !strings.HasPrefix(socket, "/") {
			return displayGPU{}, ui.Errorf("gpu_virgl_needs_the_server_s_socket", "--gpu virgl needs the server's socket", "Pass --gpu-socket with an absolute path.", "Try: --gpu virgl --gpu-socket $TMPDIR/.virgl_test")
		}
		if len(socket) > 107 || strings.ContainsAny(socket, " :") {
			return displayGPU{}, ui.Errorf("bad_gpu_socket", "Bad --gpu-socket", "'"+socket+"' is too long for a Unix socket or has a space or colon.", "Use a short path under the app's tmp.")
		}
		return displayGPU{Mode: "virgl", Socket: socket}, nil
	default:
		return displayGPU{}, ui.Errorf("unknown_gpu_mode", "Unknown --gpu mode", "'"+mode+"' isn't virgl or off.", "Try: --gpu virgl --gpu-socket <path>")
	}
}

// name is the telemetry value: "virgl" or "off".
func (g displayGPU) name() string {
	if g.Mode == "" {
		return "off"
	}
	return g.Mode
}

// up says whether the server answers on its socket. A server that's gone
// means a software start ("GPU virgl fallback no-server").
func (g displayGPU) up() bool {
	if g.Mode == "" {
		return false
	}
	c, err := net.DialTimeout("unix", g.Socket, time.Second)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// waitUp waits up to d for the server: the app starts it just before
// the desktop, and it tries the phone's EGL before it listens (it exits
// instead if that fails).
func (g displayGPU) waitUp(d time.Duration) bool {
	for end := time.Now().Add(d); ; time.Sleep(100 * time.Millisecond) {
		if g.up() {
			return true
		}
		if g.Mode == "" || time.Now().After(end) {
			return false
		}
	}
}

// bind is the ANDRONIX_BINDS entry: the server's socket at the guest's
// /tmp/.virgl_test.
func (g displayGPU) bind() string { return g.Socket + ":" + displayGPUGuestSocket }

// guestEnv prefixes the guest command, so the X session and everything it
// starts (xstartup inherits the environment) draws through VirGL. Not
// proot's ExtraEnv: these are harmless, but that goes on proot's argv.
// MESA_GL_VERSION_OVERRIDE: virgl over GLES reports a low GL version that
// many desktop apps refuse; 4.0 is what feat-gpu shipped with in Termux.
func (g displayGPU) guestEnv() string {
	return "GALLIUM_DRIVER=virpipe MESA_GL_VERSION_OVERRIDE=4.0 "
}

var reGLRenderer = regexp.MustCompile(`GL_RENDERER\s*=\s*(.+)`)

// displayGPURenderer reads the renderer from `glxgears -info` output.
func displayGPURenderer(out string) string {
	if m := reGLRenderer.FindStringSubmatch(out); m != nil {
		return strings.TrimSpace(m[1])
	}
	return ""
}

// displayGPUCheck runs glxgears -info for up to 3 s on the new display
// (as feat-gpu does: glxinfo on an unmapped window gives BadMatch with
// VirGL's readback). The result is the GPU line's status and reason:
// "ok", or "fallback <reason>" (no-glxgears means unchecked, not failed).
func displayGPUCheck(ctx context.Context, in *Inst, num int, env string) (status, reason string) {
	t := in.Target(false)
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	script := "command -v glxgears >/dev/null || { echo NO_GLXGEARS; exit 0; }; " +
		env + "DISPLAY=:" + strconv.Itoa(num) + " timeout 3 glxgears -info 2>&1 | head -20"
	c := t.Command("/bin/sh", "-c", script)
	out, _ := runWithContext(ctx, c)
	return displayGPUVerdict(string(out))
}

// displayGPUVerdict turns the check's output into the GPU line's words.
func displayGPUVerdict(out string) (status, reason string) {
	if strings.Contains(out, "NO_GLXGEARS") {
		return "ok", "unchecked"
	}
	r := displayGPURenderer(out)
	switch {
	case r == "":
		return "fallback", "no-renderer"
	case strings.Contains(strings.ToLower(r), "virgl"):
		return "ok", ""
	default:
		return "fallback", "renderer-" + strings.ToLower(strings.Fields(r)[0])
	}
}

// runWithContext runs c, killing it when ctx ends; it returns the output.
func runWithContext(ctx context.Context, c *exec.Cmd) ([]byte, error) {
	var b strings.Builder
	c.Stdout, c.Stderr = &b, &b
	if err := c.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	select {
	case err := <-done:
		return []byte(b.String()), err
	case <-ctx.Done():
		if c.Process != nil {
			c.Process.Signal(os.Kill)
		}
		<-done
		return []byte(b.String()), ctx.Err()
	}
}

// displayGPUState records the running GPU display ("<distro> <socket>"),
// so the distro's other sessions (the app's terminal, run --app) draw
// through the same server while it runs.
func displayGPUState() string { return filepath.Join(GetPaths().Home, "display-gpu.state") }

func (g displayGPU) save(distro string) {
	os.WriteFile(displayGPUState(), []byte(distro+" "+g.Socket+"\n"), 0o600)
}

func displayGPUClear() { os.Remove(displayGPUState()) }

// displayGPUFor is the running GPU display's choice if it is [distro]'s
// and its server still answers; otherwise off.
func displayGPUFor(distro string) displayGPU {
	b, err := os.ReadFile(displayGPUState())
	if err != nil {
		return displayGPU{}
	}
	f := strings.Fields(string(b))
	if len(f) != 2 || f[0] != distro {
		return displayGPU{}
	}
	g := displayGPU{Mode: "virgl", Socket: f[1]}
	if !g.up() {
		return displayGPU{}
	}
	return g
}

// applyDisplayGPU gives a session of [distro] the GPU display's socket and
// environment (not the display itself: it set them up already).
func applyDisplayGPU(t *proot.Target, distro string) {
	g := displayGPUFor(distro)
	if g.Mode == "" {
		return
	}
	if !strings.Contains(os.Getenv("ANDRONIX_BINDS"), g.bind()) {
		os.Setenv("ANDRONIX_BINDS", strings.TrimSpace(os.Getenv("ANDRONIX_BINDS")+" "+g.bind()))
	}
	if t.ExtraEnv == nil {
		t.ExtraEnv = map[string]string{}
	}
	t.ExtraEnv["GALLIUM_DRIVER"] = "virpipe"
	t.ExtraEnv["MESA_GL_VERSION_OVERRIDE"] = "4.0"
}
