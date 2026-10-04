package sys

import (
	"context"
	"encoding/binary"
	"net"
	"strings"
	"testing"
	"time"
)

// A Motorola had an /etc/resolv.conf with no usable nameserver; Go then
// asked 127.0.0.1:53 and [::1]:53 and every download failed.
func TestDNSServers(t *testing.T) {
	termux := "nameserver 8.8.4.4\n"
	for _, c := range []struct {
		name, system, want string
	}{
		{"no /etc/resolv.conf", "", "8.8.4.4:53 8.8.8.8:53 1.1.1.1:53"},
		{"empty", "# nothing\n", "8.8.4.4:53 8.8.8.8:53 1.1.1.1:53"},
		{"loopback only", "nameserver 127.0.0.1\nnameserver ::1\n", "8.8.4.4:53 8.8.8.8:53 1.1.1.1:53 127.0.0.1:53 [::1]:53"},
		{"a real one", "nameserver 192.168.1.1\nnameserver 127.0.0.1\n", "192.168.1.1:53 8.8.4.4:53 8.8.8.8:53 1.1.1.1:53 127.0.0.1:53"},
		{"junk and duplicates", "nameserver nope\nnameserver 0.0.0.0\nnameserver 8.8.8.8\n", "8.8.8.8:53 8.8.4.4:53 1.1.1.1:53"},
		{"zoned ipv6", "nameserver fe80::1%wlan0\n", "[fe80::1%wlan0]:53 8.8.4.4:53 8.8.8.8:53 1.1.1.1:53"},
	} {
		if got := strings.Join(dnsServers(c.system, termux), " "); got != c.want {
			t.Errorf("%s:\n got  %s\n want %s", c.name, got, c.want)
		}
	}
}

// The moto g57 power (Android 16) that hit it: no /etc/resolv.conf, and
// Termux's lists 127.0.0.1 first. 2.0.1 dialed it on every retry; now the
// working server comes first and loopback last.
func TestDNSServersMotoG57(t *testing.T) {
	termux := "nameserver 127.0.0.1\nnameserver 1.1.1.1\noptions edns0 trust-ad\n"
	got := dnsServers("", termux)
	if strings.Join(got, " ") != "1.1.1.1:53 8.8.8.8:53 127.0.0.1:53" {
		t.Errorf("got %v", got)
	}
	// And a lookup gets through when the loopback refuses and comes first.
	closed, _ := net.ListenPacket("udp", "127.0.0.1:0")
	dead := closed.LocalAddr().String()
	closed.Close()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer pc.Close()
	go fakeDNS(pc, net.IPv4(10, 4, 5, 6))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if addrs, err := resolverFor([]string{dead, pc.LocalAddr().String()}).LookupHost(ctx, "dl.andronix.example"); err != nil || addrs[0] != "10.4.5.6" {
		t.Errorf("lookup: %v %v", addrs, err)
	}
}

// The resolver moves on when a server refuses (a UDP dial never fails
// by itself): the first server is a closed port, the second answers.
func TestResolverFallsBack(t *testing.T) {
	closed, _ := net.ListenPacket("udp", "127.0.0.1:0")
	dead := closed.LocalAddr().String()
	closed.Close() // nothing listens there now: reads get "connection refused"

	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer pc.Close()
	go fakeDNS(pc, net.IPv4(10, 1, 2, 3))

	r := resolverFor([]string{dead, pc.LocalAddr().String()})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	addrs, err := r.LookupHost(ctx, "products.andronix.example")
	if err != nil || len(addrs) == 0 || addrs[0] != "10.1.2.3" {
		t.Fatalf("lookup through the fallback: %v %v", addrs, err)
	}
}

// fakeDNS answers every A query with ip and every other query with no
// records.
func fakeDNS(pc net.PacketConn, ip net.IP) {
	buf := make([]byte, 512)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			return
		}
		q := buf[:n]
		if len(q) < 12 {
			continue
		}
		// End of the question: the name, then type and class.
		i := 12
		for i < len(q) && q[i] != 0 {
			i += int(q[i]) + 1
		}
		if i+5 > len(q) {
			continue
		}
		qtype := binary.BigEndian.Uint16(q[i+1:])
		question := q[12 : i+5]
		resp := make([]byte, 12, 64+len(question))
		copy(resp, q[:2])                            // id
		binary.BigEndian.PutUint16(resp[2:], 0x8180) // response, recursion available
		binary.BigEndian.PutUint16(resp[4:], 1)      // one question
		resp = append(resp, question...)
		if qtype == 1 {
			binary.BigEndian.PutUint16(resp[6:], 1) // one answer
			resp = append(resp, 0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 60, 0, 4)
			resp = append(resp, ip.To4()...)
		}
		pc.WriteTo(resp, from)
	}
}
