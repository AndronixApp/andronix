package ui

import (
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func capture(f func()) string {
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	f()
	w.Close()
	os.Stdout = old
	b, _ := io.ReadAll(r)
	return string(b)
}

// Boxes and paragraphs at phone widths (Termux is ~60 columns): nothing
// wider than the terminal, a KV value's wrapped lines under its value
// column, other lines under their own indent, and no command or file name
// split in the middle.
func TestWrapWidths(t *testing.T) {
	for _, cols := range []int{40, 50, 60, 80} {
		t.Setenv("COLUMNS", strconv.Itoa(cols)) // go test's stdout isn't a terminal
		box := Box(BoxOK, "Debian 13 is ready",
			KV("Desktop", "andronix desktop debian"),
			KV("", "in Termux; it opens in the Termux:X11 app (github.com/termux/termux-x11, nightly: termux-x11-universal-debug.apk)"),
			"",
			KV("Terminal", "you're going into Debian's terminal now (type exit to leave)"),
			KV("", "next time, from Termux: ./start-debian.sh"),
			KV("Or VNC", "vncserver-start inside Debian, then localhost:1 in a VNC viewer"),
			"  an indented line that is long enough to wrap at every one of these widths, twice",
			"A plain paragraph that also has to wrap on a phone, and keeps going for a while longer.")
		out := box + capture(func() {
			Info("Downloading Debian 13 for your phone, then checking its sha256 against the one the server lists")
			Note("Files in /sdcard are not touched. Your bind settings in ~/.andronix/binds were kept for next time.")
			Row("--message TEXT", "one line about what happened, sent with the report to the Andronix team", 16)
		})
		plain := ansi.ReplaceAllString(out, "")
		for _, l := range strings.Split(strings.TrimRight(plain, "\n"), "\n") {
			if w := lipgloss.Width(l); w > cols-1 {
				t.Errorf("%d cols: line is %d wide: %q", cols, w, l)
			}
		}
		for _, word := range []string{"./start-debian.sh", "termux-x11-universal-debug.apk", "localhost:1", "vncserver-start", "~/.andronix/binds"} {
			if !strings.Contains(plain, word) {
				t.Errorf("%d cols: %q was split", cols, word)
			}
		}
		// Inside the box: "  │ " + text. A KV continuation starts 10 in.
		lines := strings.Split(plain, "\n")
		for i, l := range lines {
			if !strings.Contains(l, "Or VNC") || i+1 >= len(lines) {
				continue
			}
			next := strings.TrimPrefix(lines[i+1], "  │ ")
			if !strings.HasPrefix(next, strings.Repeat(" ", kvKey)) || strings.HasPrefix(next, strings.Repeat(" ", kvKey+1)) {
				t.Errorf("%d cols: 'Or VNC' continues at the wrong column: %q", cols, lines[i+1])
			}
		}
		for i, l := range lines {
			if strings.Contains(l, "an indented line") && i+1 < len(lines) {
				if next := strings.TrimPrefix(lines[i+1], "  │ "); !strings.HasPrefix(next, "  ") || strings.HasPrefix(next, "   ") {
					t.Errorf("%d cols: an indented line continues at the wrong column: %q", cols, lines[i+1])
				}
			}
		}
	}
}

func TestHangWrap(t *testing.T) {
	got := hangWrap("          vncserver-start inside Debian, then localhost:1 in a VNC viewer", 40, 10)
	want := []string{"          vncserver-start inside Debian,", "          then localhost:1 in a VNC", "          viewer"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	if got := hangWrap("short", 40, 4); len(got) != 1 || got[0] != "short" {
		t.Errorf("short: %q", got)
	}
	for _, l := range hangWrap(strings.Repeat("x", 90), 30, 2) {
		if len(l) > 30 {
			t.Errorf("a word longer than the line wasn't cut: %q", l)
		}
	}
}
