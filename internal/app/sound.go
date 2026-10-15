package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/termux"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// startSound makes sure Termux's PulseAudio is up for the distro
// (termux.EnsureAudio). It never stops a start: a problem is one line on
// stderr, with the details in the Termux log. stepList (an interactive
// start, andronix desktop) may install pulseaudio, shown as a step.
// Command runs (./start-debian.sh cmd, scripts) never install packages:
// that can take minutes on an out-of-date Termux, and two starts at once
// would fight over apt. They only start sound when it's installed, and
// keep stdout clean.
func startSound(ctx context.Context, stepList bool) {
	if !sys.IsTermux() {
		return
	}
	install := termux.AudioInstallDue()
	if install && stepList {
		ui.RunSteps(ctx, []ui.Step{{Label: "Setting up sound (PulseAudio)", Run: func(ctx context.Context, r ui.Reporter) error {
			did, err := termux.EnsureAudio(ctx, true, r.Line)
			if err != nil {
				return ui.Skip("no sound for now; details in " + ui.Tilde(termux.LogFile()))
			}
			r.Detail(did)
			return nil
		}}}, nil)
		return
	}
	did, err := termux.EnsureAudio(ctx, false, nil)
	if msg := soundNote(err, filepath.Join(GetPaths().Home, "no-sound-said")); msg != "" {
		fmt.Fprintln(os.Stderr, msg)
	} else if err == nil && did != "" {
		fmt.Fprintf(os.Stderr, "andronix: sound: %s\n", did)
	}
}

// soundNote is what to tell the user when sound didn't start: once, in
// plain words, since Linux and the desktop work without it (the error is in
// the Termux log). It's said again only after sound has worked in between
// (marker is the file that remembers it was said).
func soundNote(err error, marker string) string {
	if err == nil {
		os.Remove(marker)
		return ""
	}
	if _, e := os.Stat(marker); e == nil {
		return ""
	}
	os.MkdirAll(filepath.Dir(marker), 0o755)
	os.WriteFile(marker, nil, 0o644)
	return "No sound: PulseAudio didn't start in Termux. Everything else works; details in " + ui.Tilde(termux.LogFile())
}
