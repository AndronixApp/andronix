package app

import (
	"os"
	"strings"
	"testing"
)

// install.conf's keys the Andronix app reads (docs/install-conf.md) must
// keep being written under these names.
func TestInstallConfContract(t *testing.T) {
	var src strings.Builder
	for _, f := range []string{"install.go", "common.go"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src.Write(b)
	}
	for _, k := range []string{"DISTRO", "VERSION", "EDITION", "DE_NAME", "USER"} {
		if !strings.Contains(src.String(), `in.Set("`+k+`", `) {
			t.Errorf("install.conf key %s is no longer written", k)
		}
	}
	if !strings.Contains(src.String(), `in.Set("STAGE", "done")`) {
		t.Error(`install.conf no longer gets STAGE=done`)
	}
}
