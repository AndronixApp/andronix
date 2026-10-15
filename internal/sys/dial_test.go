package sys

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
)

// Android's resolver first, Go's after it (#141): a network that drops DNS
// to public servers still resolves through Android's.
func TestLookupIPOrder(t *testing.T) {
	oldS, oldG, oldU := lookupSystem, lookupGo, useSystemDNS
	defer func() { lookupSystem, lookupGo, useSystemDNS = oldS, oldG, oldU }()
	ip := []net.IPAddr{{IP: net.ParseIP("203.0.113.7")}}
	var goCalls int
	lookupGo = func(context.Context, string) ([]net.IPAddr, error) { goCalls++; return ip, nil }

	useSystemDNS = true
	lookupSystem = func(context.Context, string) ([]net.IPAddr, error) { return ip, nil }
	if _, how, err := LookupIP(context.Background(), "dl.andronix.app"); err != nil || how != "android" || goCalls != 0 {
		t.Errorf("android ok: %q %v, go calls %d", how, err, goCalls)
	}
	lookupSystem = func(context.Context, string) ([]net.IPAddr, error) { return nil, errors.New("no such host") }
	if _, how, err := LookupIP(context.Background(), "dl.andronix.app"); err != nil || !strings.HasPrefix(how, "go after android failed: no such host") {
		t.Errorf("android fails: %q %v", how, err)
	}
	lookupGo = func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("write: operation not permitted")
	}
	if _, _, err := LookupIP(context.Background(), "dl.andronix.app"); err == nil || !strings.Contains(err.Error(), "Android's resolver: no such host; public DNS: write") {
		t.Errorf("both fail: %v", err)
	}
	useSystemDNS = false // no cgo: Go's only
	lookupGo = func(context.Context, string) ([]net.IPAddr, error) { return ip, nil }
	if _, how, _ := LookupIP(context.Background(), "x"); how != "go" {
		t.Errorf("no cgo: %q", how)
	}
}

// DialContext dials what LookupIP found and logs the resolver once per host.
func TestDialContextLogs(t *testing.T) {
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	go func() {
		for {
			c, err := l.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	oldS, oldU, oldLog := lookupSystem, useSystemDNS, DNSLog
	defer func() { lookupSystem, useSystemDNS, DNSLog = oldS, oldU, oldLog }()
	useSystemDNS = true
	lookupSystem = func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("::1")}, {IP: net.ParseIP("127.0.0.1")}}, nil
	}
	var lines []string
	DNSLog = func(f string, a ...any) { lines = append(lines, f) }
	_, port, _ := net.SplitHostPort(l.Addr().String())
	for i := 0; i < 2; i++ {
		c, err := DialContext(context.Background(), "tcp", net.JoinHostPort("test.invalid", port))
		if err != nil {
			t.Fatal(err)
		}
		c.Close()
	}
	if len(lines) != 1 {
		t.Errorf("logged %d times, want once: %v", len(lines), lines)
	}
}
