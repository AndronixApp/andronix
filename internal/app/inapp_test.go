package app

import (
	"errors"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/pkgmgr"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Owner, 10.1: in the Andronix app nothing points to Termux; the command
// line (Termux) keeps its text.
func TestInAppMessages(t *testing.T) {
	kali, _ := conf.LoadDistro("kali")
	failures := []*pkgmgr.Failure{
		{Kind: pkgmgr.KindOther, Detail: "exit status 1"},
		{Kind: pkgmgr.KindLock, Detail: "another andronix (process 42) is using it"},
		{Kind: pkgmgr.KindKilled, Detail: "signal: killed"},
		{Kind: pkgmgr.KindProotArgs, Detail: "unknown option"},
	}
	text := func(err error) string {
		var e *ui.UserError
		if !errors.As(err, &e) {
			return err.Error()
		}
		return e.Title + "\n" + e.What + "\n" + e.Fix
	}
	for _, host := range []string{"standalone", ""} {
		t.Setenv("ANDRONIX_HOST", host)
		t.Setenv("ANDRONIX_DISTRO", "")
		inApp := host == "standalone"
		if InApp() != inApp {
			t.Fatalf("host %q: InApp %v", host, InApp())
		}
		msgs := []string{text(distroBusy(kali, "install 42"))}
		for _, f := range failures {
			msgs = append(msgs, text(pkgErr("Installing LXQt desktop", f)))
		}
		for i, m := range msgs {
			switch {
			case inApp && strings.Contains(m, "Termux"):
				t.Errorf("app, message %d mentions Termux:\n%s", i, m)
			case inApp && i == 0 && !strings.Contains(m, "Andronix app"):
				t.Errorf("app, lock message doesn't name the app:\n%s", m)
			case !inApp && i == 0 && !strings.Contains(m, "another Termux session"):
				t.Errorf("Termux lock message changed:\n%s", m)
			}
		}
		if inApp {
			for _, i := range []int{2, 3} { // the lock held by another andronix, killed
				if !strings.Contains(msgs[i], "Andronix app") {
					t.Errorf("app, message %d doesn't name the app:\n%s", i, msgs[i])
				}
			}
		} else if !strings.Contains(msgs[3], "Keep Termux open") {
			t.Errorf("Termux 'killed' fix changed:\n%s", msgs[3])
		}
	}
	xfce, _ := conf.ResolveDesktop("xfce")
	none, _ := conf.ResolveDesktop("none")
	got := strings.Join(appFinishLines(kali, xfce, "alex"), "\n")
	if !strings.Contains(got, "from the Andronix app") || !strings.Contains(got, "XFCE") || strings.Contains(got, "Termux") || !strings.Contains(got, "alex") {
		t.Errorf("app finish (xfce):\n%s", got)
	}
	if got := strings.Join(appFinishLines(kali, none, ""), "\n"); strings.Contains(got, "desktop") || strings.Contains(got, "Termux") {
		t.Errorf("app finish (none):\n%s", got)
	}
}
