package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDisableFailingHooks(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "etc/apt/apt.conf.d")
	os.MkdirAll(d, 0o755)
	cnf := `APT::Update::Post-Invoke-Success {"if /usr/bin/test -w /var/lib/command-not-found/ -a -e /usr/lib/cnf-update-db; then /usr/lib/cnf-update-db > /dev/null; fi";};`
	os.WriteFile(filepath.Join(d, "50command-not-found"), []byte(cnf), 0o644)
	os.WriteFile(filepath.Join(d, "50appstream"), []byte(`APT::Update::Post-Invoke-Success {"if /usr/bin/test -w /var/cache/app-info -a -e /usr/bin/appstreamcli; then appstreamcli refresh-cache > /dev/null || true; fi";};`), 0o644)
	os.WriteFile(filepath.Join(d, "docker-clean"), []byte(`DPkg::Post-Invoke { "rm -f /var/cache/apt/archives/*.deb || true"; };`), 0o644)
	os.WriteFile(filepath.Join(d, "70debconf"), []byte(`DPkg::Pre-Install-Pkgs {"/usr/sbin/dpkg-preconfigure --apt || true";};`), 0o644)

	// apt named the command: only that file goes.
	line := "E: Problem executing scripts APT::Update::Post-Invoke-Success 'if /usr/bin/test -w /var/lib/command-not-found/ -a -e /usr/lib/cnf-update-db; then /usr/lib/cnf-update-db > /dev/null; fi'"
	if off := disableFailingHooks(root, line); strings.Join(off, ",") != "50command-not-found" {
		t.Fatalf("turned off %v", off)
	}
	if _, err := os.Stat(filepath.Join(d, "50command-not-found.disabled")); err != nil {
		t.Fatal("not renamed")
	}
	if _, err := os.Stat(filepath.Join(d, "50appstream")); err != nil {
		t.Fatal("appstream was touched")
	}
	b, _ := os.ReadFile(filepath.Join(root, "etc/andronix/apt-hooks-disabled.txt"))
	if !strings.Contains(string(b), "sudo mv /etc/apt/apt.conf.d/50command-not-found.disabled /etc/apt/apt.conf.d/50command-not-found") {
		t.Fatalf("note: %q", b)
	}
	// Only the bare line: the known ones (appstream here), not docker-clean
	// or a non-post-invoke file.
	if off := disableFailingHooks(root, "E: Sub-process returned an error code"); strings.Join(off, ",") != "50appstream" {
		t.Fatalf("bare line turned off %v", off)
	}
	for _, keep := range []string{"docker-clean", "70debconf"} {
		if _, err := os.Stat(filepath.Join(d, keep)); err != nil {
			t.Errorf("%s was touched", keep)
		}
	}
	// Nothing left to match: nothing turned off.
	if off := disableFailingHooks(root, "E: Sub-process returned an error code"); len(off) != 0 {
		t.Fatalf("again: %v", off)
	}
}
