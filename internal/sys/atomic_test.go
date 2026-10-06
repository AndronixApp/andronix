package sys

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "profile")
	os.WriteFile(p, []byte("balanced\n"), 0o600)
	if err := WriteFileAtomic(p, []byte("light\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(p)
	st, _ := os.Stat(p)
	if string(b) != "light\n" || st.Mode().Perm() != 0o644 {
		t.Errorf("got %q %v", b, st.Mode().Perm())
	}
	if ents, _ := os.ReadDir(dir); len(ents) != 1 {
		t.Errorf("temp file left: %v", ents)
	}
	if err := WriteFileAtomic(filepath.Join(dir, "missing/x"), nil, 0o644); err == nil {
		t.Error("want an error for a missing folder")
	}
}
