package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/telemetry"
	"github.com/AndronixApp/andronix-distros/internal/termux"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Install result for the Andronix app (the android contract). The app
// runs `export ANDRONIX_INSTALL_ID=<16 hex>; <install command>`; at the end
// of the install we broadcast the result to its receiver with Termux's am.
// Best effort: silent, in the background, capped at 5 s, errors ignored.
const (
	appReceiver = "studio.com.techriz.andronix/.receivers.InstallResultReceiver"
	appAction   = "studio.com.techriz.andronix.INSTALL_RESULT"
)

// The ID can be pasted by the user, so only exactly 16 hex digits go out.
var installIDRe = regexp.MustCompile(`^[0-9a-fA-F]{16}$`)

type installReport struct {
	distro, de string
	modded     bool
	step       string // the running step; on failure, the one that failed
	sent       bool
	started    time.Time
	err        error  // the failure, for its telemetry class
	outcome    string // ok, but not a new install: already_installed
	// How the rootfs came: source (image, tarball, registry, ...), the
	// image's fallback reason (image.go), and the bytes downloaded.
	source, fallback     string
	downloadBytes        int64
	downloadMS, unpackMS int64 // the download and unpack steps (an image does both in download)
	// Telemetry v2: this run's random id, the app's command id (gen_id),
	// each step that ran as key:ms, the failed step's key, the time spent
	// on the interactive user setup (kept out of duration_ms, which is
	// then machine time), and the phone's compat facts.
	attemptID, genID string
	steps            []string
	stepKey          string
	userSetupMS      int64
	cr               *compatRun
}

// newInstallReport starts a run's report with a new attempt id.
func newInstallReport(distro, de string, modded bool) *installReport {
	r := &installReport{distro: distro, de: de, modded: modded, started: time.Now(), attemptID: randomHex(16)}
	if id := os.Getenv("ANDRONIX_INSTALL_ID"); installIDRe.MatchString(id) {
		r.genID = strings.ToLower(id)
	}
	return r
}

// ids are the attempt and command ids, for install_started and install_result.
func (r *installReport) ids(props map[string]any) map[string]any {
	props["attempt_id"] = r.attemptID
	if r.genID != "" {
		props["gen_id"] = r.genID
	}
	return props
}

// facts adds the phone's facts to install_result, ok or not: proot, the
// linker, kernel (major.minor), ROM family, RAM bucket, the probe summary
// (not-probed before the setup step) and the matching compat rules.
func (r *installReport) facts(props map[string]any) {
	if v := proot.Version(); v != "" {
		props["proot_version"] = v
	}
	if sys.ViaLinker() {
		props["linker"] = true
	}
	if r.cr == nil {
		return
	}
	p := r.cr.facts.Probe
	props["kernel"], props["rom"], props["ram"], props["probe"] = p.KernelMajorMinor(), p.ROM, p.RAMBucket(), p.Summary()
	props["rules"] = strings.Join(r.cr.plan.IDs, " ")
}

// props is install_result's properties for status ok or fail.
func (r *installReport) props(status string) map[string]any {
	props := r.ids(map[string]any{"ok": status == "ok", "distro": r.distro, "de": r.de, "modded": r.modded,
		"duration_ms": time.Since(r.started).Milliseconds() - r.userSetupMS})
	if r.userSetupMS > 0 {
		props["user_setup_ms"] = r.userSetupMS
	}
	if len(r.steps) > 0 {
		props["steps_ms"] = strings.Join(r.steps, ",")
	}
	r.facts(props)
	if r.outcome != "" {
		props["outcome"] = r.outcome
	}
	if r.source != "" {
		props["source"] = r.source
	}
	if r.fallback != "" {
		props["fallback_reason"] = r.fallback
	}
	if r.downloadBytes > 0 {
		props["download_bytes"] = r.downloadBytes
	}
	if r.downloadMS > 0 {
		props["download_ms"] = r.downloadMS
	}
	if r.unpackMS > 0 {
		props["unpack_ms"] = r.unpackMS
	}
	if status != "ok" {
		props["step"], props["error_class"] = r.step, telemetry.ErrorClass(r.err)
		if r.stepKey != "" {
			props["step_key"] = r.stepKey
		}
		var ue *ui.UserError
		if errors.As(r.err, &ue) {
			if ue.Kind != "" {
				props["error_kind"] = ue.Kind
			}
			if ue.Detail != "" {
				props["error_detail"] = ue.Detail
			}
		}
	}
	addPMFailure(props)
	return props
}

// desktopStart sends desktop_start (kind x11, display or vnc) next to the
// old start event, which stays for one release so charts carry over. The
// first desktop start after an install says first, with that install's
// attempt_id.
func desktopStart(in *Inst, kind string, start map[string]any) {
	p := map[string]any{"kind": kind}
	for k, v := range start {
		p[k] = v
	}
	if id := in.Get("FIRST_DESKTOP"); id != "" {
		p["first"], p["attempt_id"] = true, id
		in.Set("FIRST_DESKTOP", "")
	}
	telemetry.Send("desktop_start", p)
}

// randomHex is n random bytes as hex.
func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// amArgs is the am command line for status "ok" or "fail", or nil when
// the app didn't ask (or the ID isn't valid).
func (r *installReport) amArgs(status string) []string {
	id := os.Getenv("ANDRONIX_INSTALL_ID")
	if !installIDRe.MatchString(id) {
		return nil
	}
	a := []string{"broadcast", "-n", appReceiver, "-a", appAction,
		"--es", "id", id, "--es", "status", status, "--es", "distro", r.distro, "--es", "de", r.de}
	if status == "fail" && r.step != "" {
		a = append(a, "--es", "step", r.step)
	}
	if r.modded {
		a = append(a, "--ez", "modded", "true")
	}
	if r.outcome != "" && status == "ok" { // the app reads it only with ok
		a = append(a, "--es", "outcome", r.outcome)
	}
	return append(a, "--es", "version", Version)
}

// send broadcasts the result once, in the foreground (up to ~12 s per
// try; Termux's am starts a Java VM, which took over the 5 s the 2.0.0
// background send allowed on a slow phone, and nothing was logged). It
// tries Termux's am, then Android's own; every try goes to termux.log
// (which outlives the per-run logs), and a line tells the user if both
// failed.
func (r *installReport) send(status string) {
	if r == nil || r.sent {
		return
	}
	r.sent = true
	telemetry.Send("install_result", r.props(status))
	if id := os.Getenv("ANDRONIX_INSTALL_ID"); id != "" && !installIDRe.MatchString(id) {
		termux.Logf("install result: ANDRONIX_INSTALL_ID %q isn't 16 hex digits; not sent", id)
	}
	args := r.amArgs(status)
	if args == nil {
		return
	}
	// Termux's am ($PREFIX/bin, else the first on PATH), then Android's.
	var tried []string
	if sys.Prefix() != "" {
		tried = append(tried, filepath.Join(sys.Prefix(), "bin/am"))
	}
	if p, err := sys.LookPath("am"); err == nil && p != "/system/bin/am" && (len(tried) == 0 || p != tried[0]) {
		tried = append(tried, p)
	}
	tried = append(tried, "/system/bin/am")
	for _, am := range tried {
		if !fileExists(am) {
			termux.Logf("install result: %s missing", am)
			continue
		}
		a := args
		if strings.HasPrefix(am, "/system/") {
			// Android's am defaults to the current user, which an app may
			// not name (INTERACT_ACROSS_USERS); its own user it may.
			a = append([]string{args[0], "--user", strconv.Itoa(os.Getuid() / 100000)}, args[1:]...)
		}
		out, err := runAm(am, a)
		termux.Logf("install result %s via %s: %v %s", status, am, err, strings.Join(strings.Fields(out), " "))
		if err == nil && amDelivered(out) {
			return
		}
	}
	ui.Note("The Andronix app wasn't told that the install finished; it still works. Details: ~/.andronix/logs/termux.log")
}

// runAm runs am with a 12 s limit and returns what it printed.
func runAm(am string, args []string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	out, err := sys.CommandContext(ctx, am, args...).CombinedOutput()
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return string(out), err
}

// amDelivered: am said it sent the broadcast ("Broadcast completed", or
// Termux's am: "Broadcast sent without waiting for result"), and no error.
func amDelivered(out string) bool {
	return (strings.Contains(out, "Broadcast completed") || strings.Contains(out, "Broadcast sent")) &&
		!strings.Contains(out, "Exception") && !strings.Contains(out, "Error")
}

func fileExists(p string) bool { _, err := os.Stat(p); return err == nil }
