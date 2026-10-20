package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecArgs(t *testing.T) {
	for in, want := range map[string]string{
		"/usr/lib/firefox-esr/firefox-esr %u":              "/usr/lib/firefox-esr/firefox-esr",
		"thunar %F":                                        "thunar",
		`sh -c "echo \"hi there\"; exec x" %U`:             `sh|-c|echo "hi there"; exec x`,
		"gimp-2.10 --name=%c %f":                           "gimp-2.10|--name=GIMP",
		"app --path %k":                                    "app|--path|/usr/share/applications/gimp.desktop",
		"pct 100%% done":                                   "pct|100%|done",
		`"/opt/My App/run" --flag`:                         "/opt/My App/run|--flag",
		`exo-open --launch\sTerminalEmulator`:              "exo-open|--launch|TerminalEmulator",
		`env GTK_THEME="Adwaita:dark" gnome-calculator %i`: "env|GTK_THEME=Adwaita:dark|gnome-calculator",
	} {
		got, err := execArgs(in, "GIMP", "/usr/share/applications/gimp.desktop")
		if err != nil || strings.Join(got, "|") != want {
			t.Errorf("%q: %q %v, want %q", in, strings.Join(got, "|"), err, want)
		}
	}
	if _, err := execArgs(`broken "quote`, "", ""); err == nil {
		t.Error("unbalanced quotes accepted")
	}
}

// The session's environment goes through the file whole (values with
// newlines and '=' too), and an app started with it gets HOME from it.
func TestSessionEnvFile(t *testing.T) {
	defer func(f string) { sessionEnvFile = f }(sessionEnvFile)
	sessionEnvFile = filepath.Join(t.TempDir(), "session.env")
	if sessionEnv() != nil {
		t.Fatal("env before the session wrote it")
	}
	env := []string{"DISPLAY=:1", "HOME=/home/tester", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/dbus-x,guid=1", "X=a\nb=c"}
	os.WriteFile(sessionEnvFile, []byte(strings.Join(env, "\x00")), 0o600)
	if got := sessionEnv(); strings.Join(got, "|") != strings.Join(env, "|") {
		t.Errorf("got %q", got)
	}
	if h := envHome(sessionEnv()); h != "/home/tester" {
		t.Errorf("home %q", h)
	}
}
