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
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/rootfs"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/telemetry"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// DisplayHost is `andronix display <distro> [--size WxH] [--max-size WxH]
// [--dpi N] [--gpu virgl --gpu-socket <path>]` on the host: the standalone app's built-in display (see
// fbhelper.go). The screen can be resized (R) up to the max size, by
// default a square of the longer side, so rotating fits. It starts
// Xvfb, the desktop and the helper inside the distro, prints
//
//	DISPLAY ready fb:<path>/Xvfb_screen0 ctl:<path>.sock size:<W>x<H>
//
// once the helper listens (with --gpu, a "GPU virgl ok|fallback <reason>"
// line first; see displaygpu.go), and stays in the foreground until the desktop
// ends or it's stopped (SIGTERM, `andronix display stop`).
func DisplayHost(ctx context.Context, name, size, maxSize, dpi, gpuMode, gpuSocket string, safe bool) (err error) {
	d, err := resolveDistro(name, "start")
	if err != nil {
		return err
	}
	in := Open(d)
	// The start event: ok with the time to ready, or the failure's class.
	t0, up, sound := time.Now(), false, false
	sendTrace("host-start", "cmd=display")
	gpu, gpuOK := displayGPU{}, false
	defer func() {
		if !up && err != nil {
			sendDisplayStart(d, in, "display", t0, false, gpu.name(), false, err)
		}
	}()
	if !in.Installed() {
		return ui.Errorf("not_installed", d.Label()+" isn't installed", "There's no desktop to show yet.", "Install it first: andronix install "+d.ID+" --de xfce")
	}
	if de, err := conf.ResolveDesktop(in.Get("DE")); err != nil || de.None() {
		return ui.Errorf("no_desktop", d.Label()+" has no desktop", "It was installed as command line only.", "Add one: andronix install "+d.ID+" --de xfce")
	}
	if size == "" {
		size = "1280x720"
	}
	if !reSize.MatchString(size) {
		return ui.Errorf("bad_screen_size", "Bad screen size", "'"+size+"' doesn't look like WIDTHxHEIGHT.", "Try: --size 800x600")
	}
	w, h, _ := parseSize(size)
	if maxSize == "" {
		l := max(w, h)
		maxSize = fmt.Sprintf("%dx%d", l, l)
	}
	if mw, mh, ok := parseSize(maxSize); !ok || mw < w || mh < h || mw > 8192 || mh > 8192 {
		return ui.Errorf("bad_maximum_size", "Bad maximum size", "'"+maxSize+"' must be WIDTHxHEIGHT, at least "+size+" and at most 8192x8192.", "Try: --max-size 2400x2400")
	}
	if dpi != "" {
		if n, err := strconv.Atoi(dpi); err != nil || n < 50 || n > 600 {
			return ui.Errorf("bad_dpi", "Bad DPI", "'"+dpi+"' isn't between 50 and 600.", "Try: --dpi 160")
		}
	}
	if gpu, err = parseDisplayGPU(gpuMode, gpuSocket); err != nil {
		return err
	}
	const num = 1
	dir := filepath.Join(GetPaths().Home, "d")
	fb := filepath.Join(dir, strconv.Itoa(num), "Xvfb_screen0")
	ctl := filepath.Join(dir, strconv.Itoa(num)+".sock")
	ready := filepath.Join(dir, strconv.Itoa(num)+".ready")
	if len(ctl) > 107 {
		return ui.Errorf("the_display_socket_path_is_too_long", "The display socket path is too long", ctl+" is "+strconv.Itoa(len(ctl))+" bytes; Unix sockets allow 107.", "Use a shorter ANDRONIX_HOME.")
	}
	if conn, err := net.DialTimeout("unix", ctl, time.Second); err == nil {
		conn.Close()
		return ui.Errorf("the_display_is_already_running", "The display is already running", "Its helper answers on "+ctl+".", "Stop it first: andronix display stop")
	}
	os.MkdirAll(dir, 0o700)
	os.Remove(ready)
	os.Remove(filepath.Join(dir, "snd")) // a stale one would read as ready

	sendTrace("host-checked")
	lg := NewLog("display-" + d.ID)
	defer lg.Close()
	// This build inside the distro (an older install's doesn't know display).
	// This build's session script and the standalone layer (compositing off).
	if de, err := conf.ResolveDesktop(in.Get("DE")); err == nil && !de.None() {
		writeXstartup(in.Rootfs, de)
	}
	sendTrace("host-xstartup-written")
	// xfwm4's defaults as a system channel (xfwm4defaults.go), before the layer copies it.
	lg.Printf("xfwm4 defaults: %s", writeXfwm4Defaults(in.Rootfs))
	ApplyStandaloneLayer(in.Rootfs) // best effort: without it the session just composites
	sendTrace("host-standalone-layer")
	if err := rootfs.InstallSelf(in.Rootfs); err != nil {
		return ui.Errorf("guest_update_failed", "Couldn't update andronix inside "+d.Label(), err.Error(), "Run andronix display "+d.ID+" again.")
	}
	sendTrace("host-install-self")
	if err := ensureXvfb(ctx, in, d, lg); err != nil {
		return err
	}
	sound = sys.Standalone()
	if sound {
		if err := ensurePulse(ctx, in, d, lg); err != nil {
			lg.Printf("sound: %v (the desktop starts without it)", err)
			sound = false
		}
	}
	os.Setenv("ANDRONIX_BINDS", strings.TrimSpace(os.Getenv("ANDRONIX_BINDS")+" "+dir+":/tmp/.andronix-d"))
	gpuEnv, gpuReason := "", ""
	if gpu.Mode != "" && safe {
		gpuReason = "safe" // safe mode renders in software
	} else if gpu.Mode != "" {
		if gpu.waitUp(5 * time.Second) {
			os.Setenv("ANDRONIX_BINDS", os.Getenv("ANDRONIX_BINDS")+" "+gpu.bind())
			gpuEnv = gpu.guestEnv()
		} else {
			gpuReason = "no-server"
		}
		lg.Printf("display: gpu %s socket %s up %v", gpu.Mode, gpu.Socket, gpuEnv != "")
	}
	sendTrace("host-packages-checked")
	t := in.Target(false)
	cmd := fmt.Sprintf("%sexec /usr/local/bin/andronix display start %s :%d --max-size %s %s </dev/null", gpuEnv, size, num, maxSize, dpi)
	if safe {
		// After a black desktop: software GL, a fresh saved session (the
		// guest moves it aside) and the light settings layer.
		if de, err := conf.ResolveDesktop(in.Get("DE")); err == nil {
			if err := writeSafeLayer(in.Rootfs, de); err != nil {
				lg.Printf("safe: layer: %v", err)
			}
		}
		cmd = strings.Replace(cmd, "exec /usr/local/bin/andronix display start ", "ANDRONIX_SAFE=1 LIBGL_ALWAYS_SOFTWARE=1 exec /usr/local/bin/andronix display start --safe ", 1)
	} else {
		os.RemoveAll(filepath.Join(in.Rootfs, "etc/xdg/andronix-safe"))
	}
	if sound {
		// Alongside Xvfb, not before it (0.25-0.6 s, sv2-startup): the host
		// waits for the PCM socket before DISPLAY ready anyway.
		// From a subshell: started with a bare & it became a child of the
		// display helper (exec), which never reaped it when it exited: a
		// zombie for the whole session, one more process for Android's
		// phantom process killer (dev20, a16).
		cmd = "( /usr/local/bin/andronix sound start </dev/null & ); " + cmd
	}
	c := t.Command("/bin/sh", "-c", cmd)
	logPath := filepath.Join(GetPaths().Logs, "display-host.log")
	if out, err := os.Create(logPath); err == nil {
		defer out.Close()
		c.Stdout, c.Stderr = out, out
	}
	if err := c.Start(); err != nil {
		return ui.Errorf("the_display_didn_t_start", "The display didn't start", err.Error(), "Run andronix display "+d.ID+" again.")
	}
	sendTrace("host-proot-started", "pid="+strconv.Itoa(c.Process.Pid))
	t.Watch(c.Process.Pid) // name lookups for the session (card #141), ending with this proot
	defer traceProcs(c.Process.Pid, filepath.Join(dir, "trace-procs.txt"))()
	done := make(chan error, 1)
	go func() { done <- c.Wait() }()
	os.WriteFile(displayState(), []byte(fmt.Sprintf("%d %s\n", os.Getpid(), d.ID)), 0o644)
	defer os.Remove(displayState())
	defer os.Remove(filepath.Join(dir, "run.sock")) // the session's launch agent is gone with it
	if gpuEnv != "" {
		gpu.save(d.ID)
		defer displayGPUClear()
	}
	lg.Printf("display: %s started (proot pid %d, %s)", d.ID, c.Process.Pid, size)

	stop := func() {
		lg.Printf("display: stopping")
		stopSession(c.Process.Pid)
		select {
		case <-done:
		case <-time.After(10 * time.Second):
		}
	}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)
	defer signal.Stop(sig)

	ok := false
	for deadline := time.Now().Add(2 * time.Minute); time.Now().Before(deadline) && !ok; {
		select {
		case err := <-done:
			b, _ := os.ReadFile(logPath)
			return ui.Errorf("the_display_didn_t_start", "The display didn't start", lastNonEmpty(string(b), 3)+" ("+fmt.Sprint(err)+")",
				"Run andronix display "+d.ID+" again. Log: ~/.andronix/logs/display-host.log")
		case <-sig:
			stop()
			return nil
		case <-ctx.Done():
			stop()
			return nil
		case <-time.After(50 * time.Millisecond):
		}
		_, e1 := os.Stat(ready)
		_, e2 := os.Stat(fb)
		ok = e1 == nil && e2 == nil
	}
	if !ok {
		stop()
		return ui.Errorf("the_display_didn_t_start", "The display didn't start", "The helper didn't answer in 2 minutes.", "Run andronix display "+d.ID+" again. Log: ~/.andronix/logs/display-host.log")
	}
	sendTrace("host-helper-ready")
	// The app's terminal watches for these exact lines: GPU, then SOUND, then DISPLAY.
	if gpu.Mode != "" {
		status := "fallback"
		if gpuEnv != "" {
			status, gpuReason = displayGPUCheck(ctx, in, num, gpuEnv)
		}
		gpuOK = status == "ok"
		fmt.Println(strings.TrimSpace("GPU " + gpu.Mode + " " + status + " " + gpuReason))
		lg.Printf("display: GPU %s %s %s", gpu.Mode, status, gpuReason)
	}
	sendTrace("host-gpu-checked")
	if sound && !soundReady(dir, 5*time.Second) {
		lg.Printf("sound: no PCM socket; the desktop runs without sound")
		sound = false
	}
	sendTrace("host-sound-ready", "sound="+strconv.FormatBool(sound))
	up = true
	sendDisplayStart(d, in, "display", t0, sound, gpu.name(), gpuOK, nil)
	safeTag := ""
	if safe {
		fmt.Printf("SAFE %s\n", strings.Join(safeKeys, ","))
		safeTag = " safe:1"
	}
	traceDone("host-display-ready", t0)
	fmt.Printf("DISPLAY ready fb:%s ctl:%s size:%s%s\n", fb, ctl, size, safeTag)
	lg.Printf("display: ready fb %s ctl %s", fb, ctl)
	select {
	case err := <-done:
		lg.Printf("display: session ended (%v)", err)
	case <-sig:
		stop()
	case <-ctx.Done():
		stop()
	}
	fmt.Println("DISPLAY stopped")
	return nil
}

// DisplayHostStop is `andronix display stop` on the host.
func DisplayHostStop() error {
	b, err := os.ReadFile(displayState())
	if err != nil {
		ui.OK("No built-in display is running.")
		return nil
	}
	if f := strings.Fields(string(b)); len(f) >= 1 {
		if pid, err := strconv.Atoi(f[0]); err == nil && pid > 0 {
			syscall.Kill(pid, syscall.SIGTERM)
			for i := 0; i < 150 && pidRunning(pid); i++ {
				time.Sleep(100 * time.Millisecond)
			}
		}
	}
	os.Remove(displayState())
	ui.OK("Stopped the built-in display.")
	return nil
}

func displayState() string { return filepath.Join(GetPaths().Home, "display-host.state") }

// ensureXvfb installs DISTRO_XVFB inside the distro if Xvfb is missing.
func ensureXvfb(ctx context.Context, in *Inst, d *conf.Distro, lg *Logger) error {
	if _, err := os.Stat(filepath.Join(in.Rootfs, "usr/bin/Xvfb")); err == nil {
		return nil
	}
	if d.Xvfb() == "" {
		return ui.Errorf("no_xvfb", "No Xvfb for "+d.Label(), "The built-in display needs Xvfb, and Andronix doesn't know its package here.", "Use VNC: andronix vnc "+d.ID)
	}
	fam, err := pkgmgr.Get(d.Family)
	if err != nil {
		return err
	}
	fmt.Println("Installing Xvfb (" + d.Xvfb() + ") for the built-in display...")
	t := in.Target(true)
	var tail []string
	keep := func(l string) {
		if tail = append(tail, l); len(tail) > 5 {
			tail = tail[1:]
		}
	}
	err = t.Run(ctx, fam.Install([]string{d.Xvfb()}), nil, keep)
	if err != nil && fam.Update != "" {
		t.Run(ctx, fam.Update, nil, keep)
		err = t.Run(ctx, fam.Install([]string{d.Xvfb()}), nil, keep)
	}
	lg.Printf("display: install %s: %v", d.Xvfb(), err)
	if err != nil {
		return ui.Errorf("couldn_t_install_xvfb", "Couldn't install Xvfb", strings.Join(tail, " / "), "Check your internet, then run andronix display "+d.ID+" again.")
	}
	return nil
}

// sendDisplayStart is the start event of the app's display path (display
// or vnc): ok and the time to the ready line, whether sound came up, or
// the failure's class. gpu: "virgl" or "off" (display only; "" leaves it
// out), and gpu_ok when it's on (both need products-api's allow-list).
func sendDisplayStart(d *conf.Distro, in *Inst, mode string, t0 time.Time, sound bool, gpu string, gpuOK bool, err error) {
	p := map[string]any{"distro": d.ID, "de": in.Get("DE"), "display": mode, "ok": err == nil,
		"duration_ms": time.Since(t0).Milliseconds()}
	if gpu != "" {
		p["gpu"] = gpu
	}
	if gpu != "" && gpu != "off" {
		p["gpu_ok"] = gpuOK
	}
	if err == nil {
		p["sound"] = sound
	} else {
		p["error_class"] = telemetry.ErrorClass(err)
	}
	telemetry.Send("start", p)
	desktopStart(in, mode, p)
}
