package ui

import "testing"

func TestPasswordDesc(t *testing.T) {
	old := ASCII
	defer func() { ASCII = old }()
	ASCII = false
	if got, want := passwordDesc("Used for sudo."), "Used for sudo. Each character shows as *."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	ASCII = true
	if got, want := passwordDesc(""), "Nothing shows while you type; that's normal."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
