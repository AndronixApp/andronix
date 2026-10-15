package ui

import (
	"errors"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Theme is huh in Andronix colours.
func Theme() *huh.Theme {
	t := huh.ThemeBase()
	t.Focused.Base = t.Focused.Base.BorderForeground(Orange)
	t.Focused.Title = t.Focused.Title.Foreground(Orange).Bold(true)
	t.Focused.Description = t.Focused.Description.Foreground(Muted)
	t.Focused.ErrorIndicator = t.Focused.ErrorIndicator.Foreground(Red)
	t.Focused.ErrorMessage = t.Focused.ErrorMessage.Foreground(Red)
	t.Focused.SelectSelector = t.Focused.SelectSelector.Foreground(Orange).SetString("> ")
	t.Focused.Option = t.Focused.Option.Foreground(lipgloss.NoColor{})
	t.Focused.SelectedOption = t.Focused.SelectedOption.Foreground(Amber)
	t.Focused.FocusedButton = t.Focused.FocusedButton.Foreground(lipgloss.Color("#1a1612")).Background(Orange).Bold(true)
	t.Focused.BlurredButton = t.Focused.BlurredButton.Foreground(Muted).Background(Grey)
	t.Focused.TextInput.Cursor = t.Focused.TextInput.Cursor.Foreground(Amber)
	t.Focused.TextInput.Prompt = t.Focused.TextInput.Prompt.Foreground(Orange)
	t.Focused.TextInput.Placeholder = t.Focused.TextInput.Placeholder.Foreground(Grey)
	t.Blurred = t.Focused
	t.Blurred.Base = t.Focused.Base.BorderStyle(lipgloss.HiddenBorder())
	t.Blurred.Title = t.Blurred.Title.Foreground(Muted).Bold(false)
	return t
}

// noTerminal is a question asked with no terminal to answer it in (a new
// error each time: callers fill in its Log).
// Prompts read os.Stdin, never /dev/tty: bubbletea's fallback to opening
// /dev/tty failed with "could not open a new TTY: open /dev/tty: no such
// device or address" in a session without a controlling terminal (#255).
func noTerminal() *UserError {
	return &UserError{Class: "needs_a_terminal", Title: "This needs a terminal",
		What: "Andronix has a question for you, but its input isn't a terminal, so there's nowhere to type the answer.",
		Fix:  "Run the same command in Termux's own window (type or paste it there), not from a script or another app."}
}

func run(fields ...huh.Field) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return noTerminal()
	}
	f := huh.NewForm(huh.NewGroup(fields...)).WithTheme(Theme()).WithWidth(W() - 2).WithShowHelp(false).WithInput(os.Stdin)
	if ASCII {
		f = f.WithAccessible(true)
	}
	err := f.Run()
	if errors.Is(err, huh.ErrUserAborted) {
		return ErrCancelled
	}
	return err
}

// Confirm asks a yes/no question. Without a terminal it returns def
// (or yes with --yes).
func Confirm(title, desc string, def bool) (bool, error) {
	if Yes {
		return true, nil
	}
	if !Interactive {
		return def, nil
	}
	v := def
	err := run(huh.NewConfirm().Title(title).Description(desc).Affirmative("Yes").Negative("No").Value(&v))
	return v, err
}

// Option is a menu entry.
type Option struct{ Label, Value string }

// Choose shows a menu; without a terminal it returns def.
func Choose(title, desc string, opts []Option, def string) (string, error) {
	if !Interactive {
		return def, nil
	}
	v := def
	var hopts []huh.Option[string]
	for _, o := range opts {
		hopts = append(hopts, huh.NewOption(o.Label, o.Value))
	}
	err := run(huh.NewSelect[string]().Title(title).Description(desc).Options(hopts...).Value(&v))
	return v, err
}

// Input asks for text, checked by validate (may be nil).
func Input(title, desc, placeholder string, validate func(string) error) (string, error) {
	if !Interactive {
		return "", fmt.Errorf("%s: needs a terminal", title)
	}
	var v string
	in := huh.NewInput().Title(title).Description(desc).Placeholder(placeholder).Value(&v)
	if validate != nil {
		in = in.Validate(validate)
	}
	err := run(in)
	return v, err
}

// Password asks twice and checks both match. A mismatch says so and
// asks for both again (support #226: "Type it again" alone confused).
func Password(title, desc string, min, max int) (string, error) {
	if !Interactive {
		return "", fmt.Errorf("%s: needs a terminal", title)
	}
	check := func(s string) error {
		if len(s) < min {
			return fmt.Errorf("use at least %d characters", min)
		}
		if max > 0 && len(s) > max {
			return fmt.Errorf("use at most %d characters", max)
		}
		return nil
	}
	for {
		var a, b string
		err := run(
			huh.NewInput().Title(title).Description(passwordDesc(desc)).EchoMode(huh.EchoModePassword).Validate(check).Value(&a),
			huh.NewInput().Title("Type the same password again").Description("To catch typos.").EchoMode(huh.EchoModePassword).Value(&b),
		)
		if err != nil || a == b {
			return a, err
		}
		Warn("The two passwords didn't match. Type both again.")
	}
}

// passwordDesc adds what the typing looks like: stars, or nothing at all
// in the plain (ASCII) prompts, which read like sudo's.
func passwordDesc(desc string) string {
	hint := "Each character shows as *."
	if ASCII {
		hint = "Nothing shows while you type; that's normal."
	}
	if desc == "" {
		return hint
	}
	return desc + " " + hint
}
