package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// A sound failure is said once, plainly, and again only after sound worked.
func TestSoundNote(t *testing.T) {
	m := filepath.Join(t.TempDir(), "no-sound-said")
	fail := errors.New("exit status 1")
	if got := soundNote(fail, m); !strings.HasPrefix(got, "No sound: ") {
		t.Errorf("first failure: %q", got)
	}
	if got := soundNote(fail, m); got != "" {
		t.Errorf("second failure said again: %q", got)
	}
	soundNote(nil, m)
	if got := soundNote(fail, m); got == "" {
		t.Error("a failure after sound worked isn't said")
	}
}
