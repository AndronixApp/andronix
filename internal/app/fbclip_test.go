package app

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadCapped(t *testing.T) {
	long := strings.Repeat("a", 200)
	rd := bufio.NewReaderSize(strings.NewReader("M 1 2\r\n"+long+"\nC x\n"), 16)
	for _, want := range []string{"M 1 2", long[:50], "C x"} {
		got, err := readCapped(rd, 50)
		if err != nil || got != want {
			t.Fatalf("got %q %v, want %q", got, err, want)
		}
	}
	if _, err := readCapped(rd, 50); err == nil {
		t.Fatal("no error at the end")
	}
}

func TestLatin1(t *testing.T) {
	if got := string(latin1ToUTF8([]byte{'c', 'a', 'f', 0xe9})); got != "café" {
		t.Errorf("latin1ToUTF8: %q", got)
	}
	if got := string(utf8ToLatin1([]byte("café €"))); got != "caf\xe9 ?" {
		t.Errorf("utf8ToLatin1: %q", got)
	}
}

func TestCutUTF8(t *testing.T) {
	if got := string(cutUTF8([]byte("ab✓"), 4)); got != "ab" { // ✓ is 3 bytes
		t.Errorf("split a character: %q", got)
	}
	if got := string(cutUTF8([]byte("abcdef"), 4)); got != "abcd" {
		t.Errorf("%q", got)
	}
	if got := string(cutUTF8([]byte("ab"), 4)); got != "ab" {
		t.Errorf("%q", got)
	}
}

func TestCutUTF8Middle(t *testing.T) {
	// The cut falls right after a whole multi-byte character.
	if got := string(cutUTF8([]byte("a✓b✓"), 5)); got != "a✓b" {
		t.Errorf("%q", got)
	}
}
