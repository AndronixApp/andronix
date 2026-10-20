package app

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"io"
	"strings"
	"testing"
)

func TestBackupIDPeeks(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "debian/", Typeflag: tar.TypeDir, Mode: 0o755})
	tw.WriteHeader(&tar.Header{Name: "debian/install.conf", Typeflag: tar.TypeReg, Mode: 0o644, Size: 14})
	tw.Write([]byte("DISTRO=debian\n"))
	tw.Close()
	gz.Close()
	whole := buf.Len()
	br := bufio.NewReaderSize(bytes.NewReader(buf.Bytes()), 1<<20)
	if id := backupID(br); id != "debian" {
		t.Fatalf("id %q", id)
	}
	// Nothing consumed: the stream still unpacks from its start.
	rest, _ := io.ReadAll(br)
	if len(rest) != whole {
		t.Fatalf("peek consumed %d bytes", whole-len(rest))
	}
	if id := backupID(bufio.NewReader(strings.NewReader("not a backup"))); id != "" {
		t.Fatalf("garbage gave %q", id)
	}
}
