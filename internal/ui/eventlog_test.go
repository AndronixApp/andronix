package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEventLog(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	oldJSON, oldOut := JSON, jsonOut
	JSON, jsonOut = true, f
	defer func() { JSON, jsonOut, eventLog, pending = oldJSON, oldOut, nil, nil }()

	Emit("hello", map[string]any{"v": 1})
	Emit("progress", map[string]any{"n": 1, "cur": 1})
	Emit("progress", map[string]any{"n": 1, "cur": 2}) // within 10 s: stdout only
	var got []string
	SetEventLog(func(s string) { got = append(got, s) })
	Emit("error", map[string]any{"class": "download"})

	want := []string{`{"t":"hello","v":1}`, `{"cur":1,"n":1,"t":"progress"}`, `{"class":"download","t":"error"}`}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("log got\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	b, _ := os.ReadFile(f.Name())
	if n := strings.Count(string(b), "\n"); n != 4 {
		t.Errorf("stdout has %d events, want 4", n)
	}
}
