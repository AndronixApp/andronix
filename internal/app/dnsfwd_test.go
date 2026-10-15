package app

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/sys"
)

func TestSetNSSLine(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nsswitch.conf")
	for _, c := range []struct{ name, in, want string }{
		{"debian", "passwd: files\nhosts:          files dns\nnetworks: files\n", "passwd: files\n" + nssLine + "\nnetworks: files\n"},
		{"fedora", "hosts: files myhostname resolve [!UNAVAIL=return] dns\n", nssLine + "\n"},
		{"two hosts lines", "hosts: files\nhosts: dns\n", nssLine + "\n"},
		{"no hosts line", "passwd: files\n", "passwd: files\n" + nssLine + "\n"},
		{"already ours", nssLine + "\n", nssLine + "\n"},
	} {
		os.WriteFile(p, []byte(c.in), 0o644)
		if err := setNSSLine(p); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(p); string(b) != c.want {
			t.Errorf("%s:\n%q\nwant\n%q", c.name, b, c.want)
		}
	}
	os.Remove(p)
	if err := setNSSLine(p); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != nssLine+"\n" {
		t.Errorf("missing file: %q", b)
	}
}

func TestAnswerLookup(t *testing.T) {
	old := lookupForGuest
	defer func() { lookupForGuest = old }()
	lookups = sync.Map{}
	lookupForGuest = func(_ context.Context, host string) ([]net.IPAddr, error) {
		switch host {
		case "nope.invalid":
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		case "down.example":
			return nil, &net.DNSError{Err: "timeout", Name: host, IsTimeout: true}
		}
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.5")}, {IP: net.ParseIP("2001:db8::5")}}, nil
	}
	for _, c := range []struct {
		fam  byte
		name string
		want string
	}{
		{'0', "dl.andronix.app", "OK 203.0.113.5 2001:db8::5\n"},
		{'4', "dl.andronix.app", "OK 203.0.113.5\n"},
		{'6', "dl.andronix.app", "OK 2001:db8::5\n"},
		{'0', "nope.invalid", "NOTFOUND\n"},
		{'0', "down.example", "ERR\n"},
		{'0', "", "NOTFOUND\n"},
		{'0', "a b", "NOTFOUND\n"},
	} {
		if got := answerLookup(c.fam, c.name); got != c.want {
			t.Errorf("%c %q: %q, want %q", c.fam, c.name, got, c.want)
		}
	}
}

// The forwarder announces its port, answers, and when the session's
// process ends it removes the file and stops; a crashed one's file is
// cleaned by the next.
func TestDNSForwardLifetime(t *testing.T) {
	old := lookupForGuest
	defer func() { lookupForGuest = old }()
	lookups = sync.Map{}
	lookupForGuest = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("198.51.100.1")}}, nil
	}
	root := t.TempDir()
	dir := filepath.Join(root, "run/andronix/dns")
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, "1"), []byte("999999\n"), 0o644) // a dead forwarder's file
	if _, err := os.Stat("/proc/self/cmdline"); err == nil {
		// a killed forwarder's pid now used by another process (this test)
		os.WriteFile(filepath.Join(dir, "2"), []byte(strconv.Itoa(os.Getpid())+"\n"), 0o644)
	}

	session := sys.Command("sleep", "2")
	if err := session.Start(); err != nil {
		t.Fatal(err)
	}
	go session.Wait()
	done := make(chan error, 1)
	go func() { done <- DNSForward(root, session.Process.Pid, nil) }()

	var port string
	for i := 0; i < 50 && port == ""; i++ {
		time.Sleep(50 * time.Millisecond)
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			if e.Name() != "1" && e.Name() != "2" {
				port = e.Name()
			}
		}
	}
	for _, n := range []string{"1", "2"} {
		if _, err := os.Stat(filepath.Join(dir, n)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("the stale file %s is still there", n)
		}
	}
	c, err := net.Dial("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("port %q: %v", port, err)
	}
	fmt.Fprint(c, "4 dl.andronix.app\n")
	line, _ := bufio.NewReader(c).ReadString('\n')
	c.Close()
	if line != "OK 198.51.100.1\n" {
		t.Errorf("answer %q", line)
	}
	select {
	case <-done:
	case <-time.After(6 * time.Second):
		t.Fatal("the forwarder outlived its session")
	}
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Errorf("left %d port files", len(es))
	}
	if !strings.HasPrefix(nssLine, "hosts: files andronix [NOTFOUND=return] dns") {
		t.Error(nssLine)
	}
}

// A slow Android answer never holds the module up: ERR at once (the
// module falls back to dns), and the answer is there for the next ask.
func TestSlowLookupCached(t *testing.T) {
	old := lookupForGuest
	defer func() { lookupForGuest = old }()
	lookups = sync.Map{}
	lookupForGuest = func(context.Context, string) ([]net.IPAddr, error) {
		time.Sleep(1200 * time.Millisecond)
		return []net.IPAddr{{IP: net.ParseIP("192.0.2.9")}}, nil
	}
	s := time.Now()
	if got := answerLookup('4', "slow.example"); got != "ERR\n" || time.Since(s) > time.Second {
		t.Errorf("first: %q after %v", got, time.Since(s))
	}
	time.Sleep(700 * time.Millisecond)
	if got := answerLookup('4', "slow.example"); got != "OK 192.0.2.9\n" {
		t.Errorf("second: %q", got)
	}
}

// The names a distro looks up are the user's browsing: the forwarder never
// logs them (DNSLog, termux.log or any file under the home), answered or
// failed.
func TestForwarderLogsNoNames(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANDRONIX_HOME", filepath.Join(home, ".andronix"))
	oldLog, oldLookup := sys.DNSLog, lookupForGuest
	defer func() { sys.DNSLog, lookupForGuest = oldLog, oldLookup }()
	var logged []string
	sys.DNSLog = func(f string, a ...any) { logged = append(logged, fmt.Sprintf(f, a...)) }
	lookups = sync.Map{}
	lookupForGuest = func(_ context.Context, host string) ([]net.IPAddr, error) {
		if host == "private-ok.example" {
			return []net.IPAddr{{IP: net.ParseIP("192.0.2.7")}}, nil
		}
		return nil, &net.DNSError{Err: "timeout", Name: host, IsTimeout: true}
	}
	for _, n := range []string{"private-ok.example", "private-fail.example"} {
		answerLookup('0', n)
	}
	startSession(filepath.Join(home, "rootfs"), os.Getpid()) // may log a failed start
	for _, l := range logged {
		if strings.Contains(l, "private-") {
			t.Errorf("DNSLog got a looked-up name: %q", l)
		}
	}
	filepath.Walk(home, func(p string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() {
			if b, _ := os.ReadFile(p); strings.Contains(string(b), "private-") {
				t.Errorf("%s has a looked-up name", p)
			}
		}
		return nil
	})
}
