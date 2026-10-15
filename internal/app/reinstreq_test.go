package app

import (
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// reinstreqFix with stand-ins for dpkg-query, apt-get and dpkg: it picks the
// packages dpkg flags (R = reinst-required, H = half-installed), reinstalls
// them, removes any still flagged, then configures the rest (support #201).
func TestReinstreqFix(t *testing.T) {
	for _, c := range []struct {
		name, before, after string
		want                []string
	}{
		{"reinstall fixes it",
			"ii |nano\niRR|cpp-16-aarch64-linux-gnu\niU |xfce4\n", "ii |nano\niU |xfce4\n",
			[]string{"apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -y install --reinstall cpp-16-aarch64-linux-gnu", "dpkg --configure -a"}},
		{"still flagged: removed",
			"iHR|cpp-16\nii |nano\n", "iHR|cpp-16\n",
			[]string{"apt-get -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -y install --reinstall cpp-16", "dpkg --remove --force-remove-reinstreq --force-depends cpp-16", "dpkg --configure -a"}},
		{"nothing flagged", "ii |nano\n", "ii |nano\n", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			d := t.TempDir()
			calls := filepath.Join(d, "calls")
			write := func(name, body string) {
				if err := os.WriteFile(filepath.Join(d, name), []byte("#!/bin/sh\n"+body), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			os.WriteFile(filepath.Join(d, "state"), []byte(c.before), 0o644)
			os.WriteFile(filepath.Join(d, "after"), []byte(c.after), 0o644)
			write("dpkg-query", `cat "$D/state"`+"\n")
			write("apt-get", `echo "apt-get $*" >>"$D/calls"; cp "$D/after" "$D/state"`+"\n")
			write("dpkg", `echo "dpkg $*" >>"$D/calls"`+"\n")
			cmd := sys.Command("/bin/sh", "-c", reinstreqFix)
			cmd.Env = append(os.Environ(), "D="+d, "PATH="+d+":/usr/bin:/bin")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			b, _ := os.ReadFile(calls)
			got := strings.Split(strings.TrimSpace(string(b)), "\n")
			if len(c.want) == 0 && len(b) == 0 {
				return
			}
			if strings.Join(got, "\n") != strings.Join(c.want, "\n") {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(c.want, "\n"))
			}
		})
	}
}
