package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/rootfs"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// VNCHost is `andronix vnc <distro> [--port 5901] [--size WxH]` on the host:
// the desktop on a VNC server, for the standalone app's own viewer (no
// Termux:X11 there). It starts Xvnc and the session inside the distro
// (vncserver-start), listening on 127.0.0.1 only, prints
// "VNC ready 127.0.0.1:<port>" once the port answers, and stays in the
// foreground until the desktop ends, it's stopped (Ctrl-C, SIGTERM,
// `andronix vnc stop`), or the session's shell is killed. The password
// comes from ANDRONIX_VNC_PASSWORD (6 to 8 characters).
//
// With unix set (the standalone app's default), Xvnc listens only on a Unix
// socket in the app's private storage ($ANDRONIX_HOME/v/<display>; short,
// since socket paths are limited to 107 bytes) and the line is
// "VNC ready unix:<path>". dpi is optional; the frame rate is 30 and the
// framebuffer isn't compared (loopback needs neither).
func VNCHost(ctx context.Context, name, port, size, dpi string, unix bool) (err error) {
	d, err := resolveDistro(name, "start")
	if err != nil {
		return err
	}
	in := Open(d)
	// The start event: ok with the time to ready, or the failure's class.
	t0, up, sound := time.Now(), false, false
	sendTrace("host-start", "cmd=vnc")
	defer func() {
		if !up && err != nil {
			sendDisplayStart(d, in, "vnc", t0, false, "", false, err)
		}
	}()
	if !in.Installed() {
		return ui.Errorf("not_installed", d.Label()+" isn't installed", "There's no desktop to show yet.", "Install it first: andronix install "+d.ID+" --de xfce")
	}
	if de, err := conf.ResolveDesktop(in.Get("DE")); err != nil || de.None() {
		return ui.Errorf("no_desktop", d.Label()+" has no desktop", "It was installed as command line only.", "Add one: andronix install "+d.ID+" --de xfce")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 5901 || p > 5999 {
		return ui.Errorf("bad_vnc_port", "Bad VNC port", "'"+port+"' isn't between 5901 and 5999.", "Try: andronix vnc "+d.ID+" --port 5901")
	}
	num := p - 5900
	if size == "" {
		size = "1280x720"
	}
	if !reSize.MatchString(size) {
		return ui.Errorf("bad_screen_size", "Bad screen size", "'"+size+"' doesn't look like WIDTHxHEIGHT.", "Try: --size 1280x720")
	}
	pw := os.Getenv("ANDRONIX_VNC_PASSWORD")
	if n := len(pw); n < 6 || n > 8 {
		return ui.Errorf("no_vnc_password", "No VNC password", "Set ANDRONIX_VNC_PASSWORD to 6 to 8 characters.", "ANDRONIX_VNC_PASSWORD=secret12 andronix vnc "+d.ID)
	}
	if dpi != "" {
		if n, err := strconv.Atoi(dpi); err != nil || n < 50 || n > 600 {
			return ui.Errorf("bad_dpi", "Bad DPI", "'"+dpi+"' isn't between 50 and 600.", "Try: --dpi 160")
		}
	}
	network, addr := "tcp", "127.0.0.1:"+port
	sockDir := filepath.Join(GetPaths().Home, "v")
	if unix {
		network, addr = "unix", filepath.Join(sockDir, strconv.Itoa(num))
		if len(addr) > 107 {
			return ui.Errorf("the_vnc_socket_path_is_too_long", "The VNC socket path is too long", addr+" is "+strconv.Itoa(len(addr))+" bytes; Unix sockets allow 107.",
				"Use a shorter ANDRONIX_HOME.")
		}
		os.MkdirAll(sockDir, 0o700)
		os.Remove(addr)
		// The guest's /tmp/.andronix-vnc is this folder.
		os.Setenv("ANDRONIX_BINDS", strings.TrimSpace(os.Getenv("ANDRONIX_BINDS")+" "+sockDir+":/tmp/.andronix-vnc"))
	}
	// The standalone app's sound socket lives in <home>/d, as for display.
	sndDir := filepath.Join(GetPaths().Home, "d")
	sound = sys.Standalone()
	if sound {
		os.MkdirAll(sndDir, 0o700)
		os.Remove(filepath.Join(sndDir, "snd")) // a stale one would read as ready
		os.Setenv("ANDRONIX_BINDS", strings.TrimSpace(os.Getenv("ANDRONIX_BINDS")+" "+sndDir+":/tmp/.andronix-d"))
	}
	if conn, err := net.DialTimeout(network, addr, time.Second); err == nil {
		conn.Close()
		return ui.Errorf("vnc_is_already_running", "VNC is already running", "Something already listens on "+addr+".", "Stop it first: andronix vnc stop")
	}

	// The in-distro andronix (vncserver-start, the session) must be this
	// build: one from an older install doesn't know the Unix socket and
	// would open TCP 5901 instead.
	// This build's session script and the standalone layer (compositing off).
	if de, err := conf.ResolveDesktop(in.Get("DE")); err == nil && !de.None() {
		writeXstartup(in.Rootfs, de)
	}
	// xfwm4's defaults as a system channel (xfwm4defaults.go), before the layer copies it.
	xfwm4Status := writeXfwm4Defaults(in.Rootfs)
	ApplyStandaloneLayer(in.Rootfs) // best effort: without it the session just composites
	sendTrace("host-standalone-layer")
	if err := rootfs.InstallSelf(in.Rootfs); err != nil {
		return ui.Errorf("guest_update_failed", "Couldn't update andronix inside "+d.Label(), err.Error(), "Run andronix vnc "+d.ID+" again.")
	}
	sendTrace("host-install-self")
	t := in.Target(false)
	// The password is set fresh on every start (the app may change it);
	// the shell stays as long as Xvnc does, since proot ends the whole
	// session (--kill-on-exit) when its first process exits.
	vncEnv := "ANDRONIX_VNC_PASSWORD=" + shellQuote(pw)
	xargs := "-FrameRate 30 -CompareFB=0"
	if dpi != "" {
		xargs += " -dpi " + dpi
	}
	vncEnv += " ANDRONIX_VNC_ARGS=" + shellQuote(xargs)
	if unix {
		vncEnv += " ANDRONIX_VNC_UNIX=" + shellQuote(fmt.Sprintf("/tmp/.andronix-vnc/%d", num))
	}
	lg := NewLog("vnc-" + d.ID)
	defer lg.Close()
	lg.Printf("xfwm4 defaults: %s", xfwm4Status)
	soundCmd := ""
	if sound {
		if err := ensurePulse(ctx, in, d, lg); err != nil {
			lg.Printf("sound: %v (the desktop starts without it)", err)
			sound = false
		} else {
			soundCmd = "/usr/local/bin/andronix sound start </dev/null || true\n"
		}
	}
	script := soundCmd + fmt.Sprintf(`rm -f "$HOME/.config/tigervnc/passwd" "$HOME/.vnc/passwd"
%s /usr/local/bin/andronix vnc start %s :%d </dev/null || exit 1
while [ -e /tmp/.X%d-lock ]; do sleep 3; done`, vncEnv, size, num, num)
	c := t.Command("/bin/sh", "-c", script)
	out, _ := os.Create(filepath.Join(GetPaths().Logs, "vnc-host.log"))
	if out != nil {
		defer out.Close()
		c.Stdout, c.Stderr = out, out
	}
	if err := c.Start(); err != nil {
		return ui.Errorf("the_desktop_didn_t_start", "The desktop didn't start", err.Error(), "Run andronix vnc "+d.ID+" again.")
	}
	sendTrace("host-proot-started", "pid="+strconv.Itoa(c.Process.Pid))
	t.Watch(c.Process.Pid) // name lookups for the session (card #141), ending with this proot
	defer traceProcs(c.Process.Pid, filepath.Join(GetPaths().Logs, "trace-procs-vnc.txt"))()
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	writeVNCHostState(d.ID, num)
	defer os.Remove(vncHostState())
	defer os.Remove(filepath.Join(sndDir, "run.sock")) // the session's launch agent is gone with it
	lg.Printf("vnc: %s session started (proot pid %d, display :%d, %s)", d.ID, c.Process.Pid, num, size)

	stop := func() {
		lg.Printf("vnc: stopping :%d", num)
		t.Run(context.Background(), fmt.Sprintf("/usr/local/bin/andronix vnc stop :%d", num), nil, nil)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			stopSession(c.Process.Pid)
			<-done
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sig)

	// Ready when the port answers (the first start also sets up the
	// session's files: up to two minutes on a slow phone).
	ready := false
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline) && !ready; {
		select {
		case err := <-done:
			b, _ := os.ReadFile(filepath.Join(GetPaths().Logs, "vnc-host.log"))
			return ui.Errorf("the_desktop_didn_t_start", "The desktop didn't start", lastNonEmpty(string(b), 3)+" ("+fmt.Sprint(err)+")",
				"Run andronix vnc "+d.ID+" again. Log: ~/.andronix/logs/vnc-host.log")
		case <-sig:
			stop()
			return nil
		case <-ctx.Done():
			stop()
			return nil
		case <-time.After(100 * time.Millisecond):
		}
		if conn, err := net.DialTimeout(network, addr, time.Second); err == nil {
			conn.Close()
			ready = true
		}
	}
	if !ready {
		stop()
		return ui.Errorf("the_desktop_didn_t_start", "The desktop didn't start", "Nothing answered on "+addr+" in 2 minutes.",
			"Run andronix vnc "+d.ID+" again. Log: ~/.andronix/logs/vnc-host.log")
	}
	sendTrace("host-vnc-answers")
	// The app's terminal watches for this exact line.
	if sound && !soundReady(sndDir, 5*time.Second) {
		lg.Printf("sound: no PCM socket; the desktop runs without sound")
		sound = false
	}
	up = true
	sendDisplayStart(d, in, "vnc", t0, sound, "", false, nil)
	traceDone("host-vnc-ready", t0)
	if unix {
		fmt.Printf("VNC ready unix:%s\n", addr)
	} else {
		fmt.Printf("VNC ready 127.0.0.1:%s\n", port)
	}
	lg.Printf("vnc: ready on %s %s", network, addr)
	select {
	case err := <-done:
		lg.Printf("vnc: session ended (%v)", err)
	case <-sig:
		stop()
	case <-ctx.Done():
		stop()
	}
	fmt.Println("VNC stopped")
	return nil
}

// VNCHostStop is `andronix vnc stop` on the host: stops a running VNCHost.
func VNCHostStop() error {
	b, err := os.ReadFile(vncHostState())
	if err != nil {
		ui.OK("No VNC desktop is running.")
		return nil
	}
	f := strings.Fields(string(b))
	if len(f) >= 1 {
		if pid, err := strconv.Atoi(f[0]); err == nil && pid > 0 {
			syscall.Kill(pid, syscall.SIGTERM)
			for i := 0; i < 150 && pidRunning(pid); i++ {
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	os.Remove(vncHostState())
	ui.OK("Stopped the VNC desktop.")
	return nil
}

func vncHostState() string { return filepath.Join(GetPaths().Home, "vnc-host.state") }

func writeVNCHostState(distro string, num int) {
	os.WriteFile(vncHostState(), []byte(fmt.Sprintf("%d %s %d\n", os.Getpid(), distro, num)), 0o644)
}

func pidRunning(pid int) bool { return syscall.Kill(pid, 0) == nil }

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func lastNonEmpty(s string, n int) string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return strings.Join(out, " / ")
}
