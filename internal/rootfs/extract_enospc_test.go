package rootfs

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

// TestENOSPCWrapPreservesIs is the contract the install callers rely on: the
// wrap extract() puts on a fatal entry error keeps errors.Is(ENOSPC) true, so
// the "unpack" step and unpackImage can map a full disk to the "Not enough
// space" result (#1147). A real disk-full extraction is exercised on-device in
// the emulator gate (choosing the errno isn't portable in a unit test).
func TestENOSPCWrapPreservesIs(t *testing.T) {
	wrapped := fmt.Errorf("%s: %w", "etc/some/file", &os.PathError{Op: "write", Path: "x", Err: syscall.ENOSPC})
	if !errors.Is(wrapped, syscall.ENOSPC) {
		t.Fatal("errors.Is lost ENOSPC through the wrap; install callers would miss it")
	}
}

// TestExtractContinuesOnNonFatal confirms the change did NOT make every
// per-entry error fatal: a non-ENOSPC failure (here a hard link to a missing
// target) is still warned and skipped, and later entries still extract.
func TestExtractContinuesOnNonFatal(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	// A hard link whose target doesn't exist: os.Link fails and copyFile can't
	// open the missing source — a non-fatal per-entry error.
	if err := tw.WriteHeader(&tar.Header{Name: "badlink", Linkname: "nope/missing", Typeflag: tar.TypeLink}); err != nil {
		t.Fatal(err)
	}
	// A normal file after it, which must still land on disk.
	body := []byte("hello")
	if err := tw.WriteHeader(&tar.Header{Name: "good", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()

	x := &Extractor{Root: root}
	if err := x.ExtractReader(&buf); err != nil {
		t.Fatalf("ExtractReader returned %v; a non-fatal entry error must not abort", err)
	}
	if len(x.Warnings) == 0 {
		t.Errorf("expected a warning for the bad hard link")
	}
	if b, err := os.ReadFile(filepath.Join(root, "good")); err != nil || string(b) != "hello" {
		t.Errorf("later entry didn't extract after a non-fatal error: %v / %q", err, b)
	}
}
