package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Sound in the standalone app: no Termux PulseAudio there, so the distro
// runs its own, with a silent sink the desktop plays to. Its monitor goes
// out raw (s16le, 48 kHz, stereo) on /tmp/.andronix-d/snd, a Unix socket
// in the app's private storage ($ANDRONIX_HOME/d/snd), which the app
// reads and plays with AAudio. Apps in the distro find the server at
// PULSE_SERVER=unix:/tmp/.andronix-pulse/native.

const (
	pulseDir  = "/tmp/.andronix-pulse"
	pcmSocket = "/tmp/.andronix-d/snd"
)

const pulseConfig = `# Andronix (standalone app): a silent sink; its monitor goes to the app.
load-module module-null-sink sink_name=andronix sink_properties=device.description=Andronix rate=48000 channels=2
set-default-sink andronix
load-module module-native-protocol-unix socket=` + pulseDir + `/native auth-anonymous=1
load-module module-simple-protocol-unix socket=` + pcmSocket + ` source=andronix.monitor record=true playback=false format=s16le rate=48000 channels=2
`

// No module-suspend-on-idle: a suspended sink took about 2 s to give the
// app its first bytes after it (re)connected.

// SoundStart is `andronix sound start` inside the distro: PulseAudio with
// the config above, daemonized; it returns once the PCM socket is there.
func SoundStart() error {
	if _, err := sys.LookPath("pulseaudio"); err != nil {
		return ui.Errorf("pulseaudio_isn_t_installed", "PulseAudio isn't installed", "Sound in the Andronix app needs PulseAudio in the distro.", "It's installed when the desktop starts from the app.")
	}
	if c, err := net.DialTimeout("unix", pcmSocket, time.Second); err == nil {
		c.Close()
		return nil // already running
	}
	os.MkdirAll(pulseDir, 0o700)
	os.MkdirAll(filepath.Dir(pcmSocket), 0o700)
	os.Remove(pcmSocket)
	cfg := filepath.Join(pulseDir, "andronix.pa")
	if err := os.WriteFile(cfg, []byte(pulseConfig), 0o600); err != nil {
		return err
	}
	rt := os.Getenv("XDG_RUNTIME_DIR")
	if rt == "" {
		rt = fmt.Sprintf("/tmp/runtime-%d", os.Getuid())
		os.MkdirAll(rt, 0o700)
	}
	c := sys.Command("pulseaudio", "--daemonize=yes", "-n", "-F", cfg, "--exit-idle-time=-1",
		"--disable-shm=yes", "--high-priority=no", "--realtime=no", "--log-target=file:"+pulseDir+"/pulse.log")
	c.Env = append(os.Environ(), "XDG_RUNTIME_DIR="+rt, "PULSE_RUNTIME_PATH="+pulseDir)
	if out, err := c.CombinedOutput(); err != nil {
		b, _ := os.ReadFile(pulseDir + "/pulse.log")
		return ui.Errorf("sound_didn_t_start", "Sound didn't start", strings.TrimSpace(string(out))+" "+lastNonEmpty(string(b), 2), "The desktop works without sound.")
	}
	for i := 0; i < 40; i++ {
		if _, err := os.Stat(pcmSocket); err == nil {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return ui.Errorf("sound_didn_t_start", "Sound didn't start", "PulseAudio didn't open its socket.", "The desktop works without sound. Log: "+pulseDir+"/pulse.log")
}

// ensurePulse installs DISTRO_PULSE inside the distro if pulseaudio is
// missing (first standalone desktop start).
func ensurePulse(ctx context.Context, in *Inst, d *conf.Distro, lg *Logger) error {
	if _, err := os.Stat(filepath.Join(in.Rootfs, "usr/bin/pulseaudio")); err == nil {
		return nil
	}
	if d.Pulse() == "" {
		return fmt.Errorf("no PulseAudio package for %s", d.ID)
	}
	fam, err := pkgmgr.Get(d.Family)
	if err != nil {
		return err
	}
	fmt.Println("Installing PulseAudio (" + d.Pulse() + ") for sound in the app...")
	t := in.Target(true)
	err = t.Run(ctx, fam.Install([]string{d.Pulse()}), nil, nil)
	if err != nil && fam.Update != "" {
		t.Run(ctx, fam.Update, nil, nil)
		err = t.Run(ctx, fam.Install([]string{d.Pulse()}), nil, nil)
	}
	lg.Printf("sound: install %s: %v", d.Pulse(), err)
	return err
}

// soundReady prints the SOUND line once the PCM socket answers (it's
// printed before the DISPLAY or VNC line); false means no sound.
func soundReady(dir string, wait time.Duration) bool {
	sock := filepath.Join(dir, "snd")
	for end := time.Now().Add(wait); time.Now().Before(end); time.Sleep(50 * time.Millisecond) {
		if c, err := net.DialTimeout("unix", sock, time.Second); err == nil {
			c.Close()
			fmt.Printf("SOUND ready pcm:%s rate:48000 channels:2 format:s16le\n", sock)
			return true
		}
	}
	return false
}
