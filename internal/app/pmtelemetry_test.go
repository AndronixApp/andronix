package app

import "testing"

// proot_version and linker go only on package-manager failures.
func TestAddPMFailure(t *testing.T) {
	p := map[string]any{"error_class": "download"}
	addPMFailure(p)
	if _, ok := p["proot_version"]; ok {
		t.Error("download failure: no proot_version")
	}
	if _, ok := p["linker"]; ok {
		t.Error("not started through the linker: no linker field")
	}
}
