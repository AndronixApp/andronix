package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// copyFile's errors say which side failed (ANDROID-N had only "open <path>:
// operation not permitted"), and a good copy gets its mode.
func TestCopyFileErrors(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	os.WriteFile(src, []byte("x"), 0o644)
	if err := copyFile(filepath.Join(dir, "missing"), filepath.Join(dir, "a"), 0o700); err == nil || !strings.HasPrefix(err.Error(), "read open ") {
		t.Errorf("read side: %v", err)
	}
	if err := copyFile(src, filepath.Join(dir, "no/such/dir/b"), 0o700); err == nil || !strings.HasPrefix(err.Error(), "write open ") {
		t.Errorf("write side: %v", err)
	}
	dst := filepath.Join(dir, "c")
	if err := copyFile(src, dst, 0o700); err != nil {
		t.Fatal(err)
	}
	if st, _ := os.Stat(dst); st.Mode().Perm() != 0o700 {
		t.Errorf("mode %v", st.Mode().Perm())
	}
}
