package app

import (
	"strings"
	"testing"
	"time"
)

// Already installed is a success with an outcome, for the app and telemetry.
func TestReportOutcome(t *testing.T) {
	t.Setenv("ANDRONIX_INSTALL_ID", "0123456789abcdef")
	r := &installReport{distro: "kali", de: "xfce", outcome: "already_installed"}
	a := strings.Join(r.amArgs("ok"), " ")
	if !strings.Contains(a, "--es status ok") || !strings.Contains(a, "--es outcome already_installed") {
		t.Errorf("am: %s", a)
	}
	r.outcome = ""
	if a := strings.Join(r.amArgs("ok"), " "); strings.Contains(a, "outcome") {
		t.Errorf("no outcome expected: %s", a)
	}
}

// outcome goes out only with status ok, and expired tokens are caught
// before a reinstall deletes anything.
func TestOutcomeOnlyWithOK(t *testing.T) {
	t.Setenv("ANDRONIX_INSTALL_ID", "0123456789abcdef")
	r := &installReport{distro: "kali", outcome: "already_installed"}
	if a := strings.Join(r.amArgs("fail"), " "); strings.Contains(a, "outcome") {
		t.Errorf("fail with outcome: %s", a)
	}
}

func TestTokenExpired(t *testing.T) {
	now := time.Unix(1790000000, 0)
	for tok, want := range map[string]bool{"k=K&e=1789999000&h=x": true, "k=K&e=1790003600&h=x": false, "k=K&h=x": false, "garbage%%": false, "": false} {
		if got := tokenExpired(tok, now); got != want {
			t.Errorf("%q: %v", tok, got)
		}
	}
}
