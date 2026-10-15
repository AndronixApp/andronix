package ui

import (
	"errors"
	"os"
	"testing"
)

// With stdin not a terminal, a prompt fails plainly instead of opening
// /dev/tty (#255).
func TestPromptNeedsATerminal(t *testing.T) {
	r, w, _ := os.Pipe()
	defer r.Close()
	defer w.Close()
	old, oldI := os.Stdin, Interactive
	os.Stdin, Interactive = r, true
	defer func() { os.Stdin, Interactive = old, oldI }()
	check := func(what string, err error) {
		var ue *UserError
		if !errors.As(err, &ue) || ue.Class != "needs_a_terminal" {
			t.Errorf("%s: %v", what, err)
		}
	}
	_, err := Input("Username", "", "", nil)
	check("Input", err)
	_, err = Password("Password", "", 4, 0)
	check("Password", err)
}
