package app

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSoundReady(t *testing.T) {
	dir, err := os.MkdirTemp("", "snd") // short: Unix socket paths are limited
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if soundReady(dir, 300*time.Millisecond) {
		t.Fatal("ready without a socket")
	}
	// A stale socket file nobody listens on isn't ready either.
	l, err := net.Listen("unix", filepath.Join(dir, "snd"))
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if soundReady(dir, 300*time.Millisecond) {
		t.Fatal("ready with a stale socket")
	}
	os.Remove(filepath.Join(dir, "snd"))
	l, err = net.Listen("unix", filepath.Join(dir, "snd"))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	if !soundReady(dir, time.Second) {
		t.Fatal("not ready with a listening socket")
	}
}

func TestPulseConfig(t *testing.T) {
	for _, want := range []string{"socket=/tmp/.andronix-pulse/native", "socket=/tmp/.andronix-d/snd",
		"source=andronix.monitor", "format=s16le rate=48000 channels=2", "set-default-sink andronix"} {
		if !strings.Contains(pulseConfig, want) {
			t.Errorf("pulseConfig lacks %q", want)
		}
	}
}
