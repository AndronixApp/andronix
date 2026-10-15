package app

import (
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// pmSaved names the recovery that saved this run's package manager (see
// addPMFailure); empty when none was needed.
var pmSaved string

// addPMFailure adds, to a failed result's telemetry, which proot ran and
// whether andronix came through Play Termux's linker: only for
// package-manager failures (the Play Termux + kernel 4.x 'Refreshing
// package lists' reports). Fields agreed with products-api (lead):
// proot_version (string), linker (true, else absent).
//
// A result that worked only because a recovery saved it carries that
// recovery as its error_kind (pmSaved, e.g. apt_hook_retried), so we see
// how often it saves an install.
func addPMFailure(props map[string]any) {
	if props["ok"] == true && pmSaved != "" {
		props["error_kind"] = pmSaved
	}
	if props["error_class"] != "package_manager" {
		return
	}
	if v := proot.Version(); v != "" {
		props["proot_version"] = v
	}
	if sys.ViaLinker() {
		props["linker"] = true
	}
}
