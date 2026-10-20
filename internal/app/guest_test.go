package app

import "testing"

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
