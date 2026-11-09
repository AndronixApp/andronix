package app

import (
	"strings"
	"testing"
)

// card #138: the app blocks some desktops on kernels before 4.8 and says
// so through ANDRONIX_BLOCKED_DESKTOPS; Welcome doesn't send people to a
// desktop the app won't open.
func TestWelcomeDesktopLine(t *testing.T) {
	for _, c := range []struct {
		standalone, vnc, xvfb bool
		de, blocked, want     string
	}{
		{true, true, true, "xfce", "", "Desktop: open it from the Andronix app."},
		{true, true, true, "xfce", "xfce,mate,kde", "Desktop: not available on this device (needs Linux kernel 4.8 or newer). LXQt works here: add it from the Andronix app."},
		{true, false, true, "kde", "XFCE, Mate ,KDE", "Desktop: not available on this device (needs Linux kernel 4.8 or newer). LXQt works here: add it from the Andronix app."},
		{true, true, true, "xfce", "all", "Desktop: not available on this device (needs Linux kernel 4.8 or newer)."},
		{true, true, true, "lxqt", "xfce,mate,kde", "Desktop: open it from the Andronix app."},
		{true, true, true, "xfce", "mate,kde", "Desktop: open it from the Andronix app."},
		{false, true, false, "xfce", "xfce", "Desktop: type exit, then in Termux: andronix desktop debian (Termux:X11). Or VNC here: vncserver-start."},
		{true, false, false, "", "xfce", "Leave with: exit"},
	} {
		if got := welcomeDesktopLine(c.standalone, c.vnc, c.xvfb, c.de, c.blocked, "debian"); got != c.want {
			t.Errorf("%+v:\n got %q\nwant %q", c, got, c.want)
		}
	}
}

// TestXvncArgsNolistenLocal pins the #344 fix: Xvnc never listens on the X11
// abstract socket, so another app's X server on the same display can't block
// ours with "server already running". Holds on both the TCP and the standalone
// Unix-socket viewer paths.
func TestXvncArgsNolistenLocal(t *testing.T) {
	joined := func(a []string) string { return "\x00" + strings.Join(a, "\x00") + "\x00" }

	args := xvncArgs(1, "", "1", "/root/.config/tigervnc/passwd", "", nil)
	j := joined(args)
	if !strings.Contains(j, "\x00-nolisten\x00local\x00") {
		t.Fatalf("Xvnc args missing -nolisten local: %v", args)
	}
	if strings.Contains(j, "\x00-geometry\x00") { // no size => let Xvnc fit the viewer
		t.Errorf("unexpected -geometry with empty size: %v", args)
	}
	if !strings.Contains(j, "\x00-localhost=1\x00") {
		t.Errorf("listen flag not carried into -localhost: %v", args)
	}
	if !strings.Contains(j, "\x00-rfbport\x005901\x00") {
		t.Errorf("rfbport not 5900+display: %v", args)
	}

	// A size adds -geometry; extra options stay last so they can override.
	args = xvncArgs(2, "1280x720", "0", "pw", "", []string{"-extension", "MIT-SHM"})
	j = joined(args)
	if !strings.Contains(j, "\x00-nolisten\x00local\x00") {
		t.Errorf("Xvnc args missing -nolisten local: %v", args)
	}
	if !strings.Contains(j, "\x00-geometry\x001280x720\x00") {
		t.Errorf("size not passed as -geometry: %v", args)
	}
	if !strings.Contains(j, "\x00-localhost=0\x00") {
		t.Errorf("--lan listen flag not carried into -localhost: %v", args)
	}
	if got := args[len(args)-2] + " " + args[len(args)-1]; got != "-extension MIT-SHM" {
		t.Errorf("extra args not kept last, got %q", got)
	}

	// Standalone app viewer: a private RFB Unix socket, no TCP port, but still
	// -nolisten local on the X11 side.
	args = xvncArgs(1, "", "1", "pw", "/data/app/vnc.sock", nil)
	j = joined(args)
	if !strings.Contains(j, "\x00-nolisten\x00local\x00") {
		t.Errorf("Unix-socket path missing -nolisten local: %v", args)
	}
	if !strings.Contains(j, "\x00-rfbunixpath\x00/data/app/vnc.sock\x00") {
		t.Errorf("Unix-socket path not passed: %v", args)
	}
	if !strings.Contains(j, "\x00-rfbport\x00-1\x00") {
		t.Errorf("Unix-socket path should disable the TCP port: %v", args)
	}
}

// TestIsXServer covers #344 (b): a lock's pid counts as live only when it's
// still one of our X servers. Xvfb (the Termux:X11 host) must now count, and an
// unrelated process that reused the pid after a kill must not.
func TestIsXServer(t *testing.T) {
	nul := func(parts ...string) string { return strings.Join(parts, "\x00") + "\x00" }
	cases := []struct {
		name    string
		cmdline string
		want    bool
	}{
		{"Xvnc", nul("Xvnc", ":1", "-rfbport", "5901"), true},
		{"Xtigervnc", nul("/usr/bin/Xtigervnc", ":1"), true},
		{"vncserver wrapper", nul("/usr/bin/perl", "/usr/bin/vncserver", ":1"), true},
		{"Xvfb absolute path", nul("/usr/bin/Xvfb", ":1", "-screen", "0", "1280x720x24"), true},
		{"Xorg", nul("/usr/lib/xorg/Xorg", ":0"), true},
		{"bare X", nul("X", ":1"), true},
		{"reused by shell", nul("/bin/bash", "-c", "sleep 999"), false},
		{"reused by xterm", nul("xterm"), false},
		{"reused by xfce session", nul("/usr/bin/xfce4-session"), false},
		{"empty", "", false},
	}
	for _, c := range cases {
		if got := isXServer(c.cmdline); got != c.want {
			t.Errorf("%s: isXServer(%q) = %v, want %v", c.name, c.cmdline, got, c.want)
		}
	}
}
