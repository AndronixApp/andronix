package app

import (
	"github.com/AndronixApp/andronix-distros/internal/proot"
	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// addPMFailure adds, to a failed result's telemetry, which proot ran and
// whether andronix came through Play Termux's linker: only for
// package-manager failures (the Play Termux + kernel 4.x 'Refreshing
// package lists' reports). Fields agreed with products-api (lead):
// proot_version (string), linker (true, else absent).
func addPMFailure(props map[string]any) {
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
