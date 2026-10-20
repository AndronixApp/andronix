package app

import (
	"regexp"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/compat"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Telemetry v2 on install_result: the run's ids, step times, machine time
// without the user's typing, the failed step's key and the phone's facts.
func TestInstallResultV2(t *testing.T) {
	t.Setenv("ANDRONIX_INSTALL_ID", "0123456789ABCDEF")
	r := newInstallReport("debian", "xfce", false)
	if !regexp.MustCompile(`^[0-9a-f]{32}$`).MatchString(r.attemptID) || newInstallReport("debian", "", false).attemptID == r.attemptID {
		t.Fatalf("attempt_id %q", r.attemptID)
	}
	r.started = time.Now().Add(-100 * time.Second)
	r.userSetupMS = 40000
	r.steps = []string{"phone-check:812", "download:24177", "refresh:15022"}
	r.step, r.stepKey, r.err = "Refreshing package lists", "refresh", &ui.UserError{Class: "package_manager", Kind: "network_dns"}
	r.cr = &compatRun{facts: compat.Facts{Probe: &compat.Probe{Kernel: "4.14.356-perf+", ROM: "miui", RAMMB: 3500}},
		plan: &compat.Plan{IDs: []string{"mntid-old-kernel", "phantom"}}}
	p := r.props("fail")
	want := map[string]any{"attempt_id": r.attemptID, "gen_id": "0123456789abcdef", "steps_ms": "phone-check:812,download:24177,refresh:15022",
		"step_key": "refresh", "user_setup_ms": int64(40000), "kernel": "4.14", "rom": "miui", "ram": "3-4G", "probe": "not-probed",
		"rules": "mntid-old-kernel phantom", "error_class": "package_manager", "error_kind": "network_dns"}
	for k, v := range want {
		if p[k] != v {
			t.Errorf("%s = %v, want %v", k, p[k], v)
		}
	}
	if d := p["duration_ms"].(int64); d < 59000 || d > 61000 {
		t.Errorf("duration_ms %d: want about 60 s (100 s less the 40 s user setup)", d)
	}
	t.Setenv("ANDRONIX_INSTALL_ID", "pasted junk")
	if _, ok := newInstallReport("debian", "", false).ids(map[string]any{})["gen_id"]; ok {
		t.Error("gen_id from an invalid ANDRONIX_INSTALL_ID")
	}
}

// The first desktop start after an install is marked once.
func TestFirstDesktopOnce(t *testing.T) {
	dir := t.TempDir()
	in := &Inst{Dir: dir, State: dir + "/install.conf"}
	in.Set("FIRST_DESKTOP", "00112233445566778899aabbccddeeff")
	desktopStart(in, "x11", map[string]any{"distro": "debian"})
	if v := in.Get("FIRST_DESKTOP"); v != "" {
		t.Errorf("FIRST_DESKTOP still %q after the first desktop start", v)
	}
}
