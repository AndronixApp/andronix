package app

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Startup tracing, for profiling how long a desktop takes to come up. Only
// with ANDRONIX_TRACE=1; local files and stdout only, never telemetry.
//
//	TRACE <unix_ms> <step> [key=value ...]
//
// The host prints these on stdout (the app logs them on the same wall
// clock); in the guest they go wherever the process's stdout goes: the
// display's steps and window maps to $ANDRONIX_HOME/logs/display-host.log,
// session-prep's to the session log (/tmp/andronix-session-<uid>.log). All
// use the same clock, so merge them by time. Window lines are printed from
// goroutines and may come out of order: their stamp is the event's, so
// sort by it.
func tracing() bool { return os.Getenv("ANDRONIX_TRACE") == "1" }

// sendTrace prints one TRACE line when tracing.
func sendTrace(step string, kv ...string) {
	if !tracing() {
		return
	}
	line := "TRACE " + strconv.FormatInt(time.Now().UnixMilli(), 10) + " " + step
	if len(kv) > 0 {
		line += " " + strings.Join(kv, " ")
	}
	fmt.Println(line)
}

// traceDone prints a TRACE line for a step that started at t, with its length.
func traceDone(step string, t time.Time) {
	if tracing() {
		sendTrace(step, "ms="+strconv.FormatInt(time.Since(t).Milliseconds(), 10))
	}
}

// traceProcs, when tracing, follows the processes under pid (the proot
// session) until the returned stop is called (at most 3 minutes): every
// 100 ms it writes when each one first
// appeared, exec'd something else and went, as "<unix_ms> + <pid> <ppid>
// <argv0> [args]", "<unix_ms> = <pid> <ppid> <argv0> [args]" and
// "<unix_ms> - <pid> <argv0>", to out. It reads /proc from outside
// proot (same uid, no ptrace), so it costs the session nothing. argv[0] is
// the guest's path (/usr/bin/xfwm4); processes proot runs through its
// loader show up as that, raw.
func traceProcs(pid int, out string) (stop func()) {
	if !tracing() {
		return func() {}
	}
	f, err := os.Create(out)
	if err != nil {
		return func() {}
	}
	// The first minutes are the start; a long session isn't sampled for hours.
	ctx, stop := context.WithTimeout(context.Background(), 3*time.Minute)
	go func() {
		defer f.Close()
		w := bufio.NewWriter(f)
		seen := map[int]string{}
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			now := time.Now().UnixMilli()
			alive := descendants(pid)
			for p, ppid := range alive {
				args := cmdline(p)
				old, ok := seen[p]
				switch {
				case !ok:
					fmt.Fprintf(w, "%d + %d %d %s\n", now, p, ppid, args)
				case args != old && args != "":
					fmt.Fprintf(w, "%d = %d %d %s\n", now, p, ppid, args) // exec (xinitrc -> xfce4-session)
				default:
					continue
				}
				seen[p] = args
			}
			for p, a := range seen {
				if _, ok := alive[p]; !ok {
					fmt.Fprintf(w, "%d - %d %s\n", now, p, argv0(a))
					delete(seen, p)
				}
			}
			w.Flush()
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
		}
	}()
	return stop
}

// descendants maps every live process of the session under root (root
// included) to its parent. Membership is proot's tracing, not the process
// tree: a daemon (dbus-daemon --fork, pulseaudio --daemonize, the session
// agent) leaves the tree when its parent exits, but proot still traces it
// (TracerPid == root). Processes under root by ppid count too (root itself,
// whose TracerPid is 0).
func descendants(root int) map[int]int {
	parent := map[int]int{}
	traced := map[int]bool{}
	ents, _ := os.ReadDir("/proc")
	for _, e := range ents {
		p, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		b, err := os.ReadFile(filepath.Join("/proc", e.Name(), "status"))
		if err != nil {
			continue // another uid's
		}
		var ppid, tracer int
		for _, l := range strings.Split(string(b), "\n") {
			if v, ok := strings.CutPrefix(l, "PPid:"); ok {
				ppid, _ = strconv.Atoi(strings.TrimSpace(v))
			} else if v, ok := strings.CutPrefix(l, "TracerPid:"); ok {
				tracer, _ = strconv.Atoi(strings.TrimSpace(v))
			}
		}
		parent[p] = ppid
		traced[p] = tracer == root
	}
	out := map[int]int{}
	for p, pp := range parent {
		if traced[p] {
			out[p] = pp
			continue
		}
		for q := p; q > 1; q = parent[q] {
			if q == root {
				out[p] = pp
				break
			}
			if _, ok := parent[q]; !ok {
				break
			}
		}
	}
	return out
}

// cmdline is a process's arguments, space-joined and cut to 160 bytes.
func cmdline(pid int) string {
	b, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cmdline"))
	s := strings.TrimSpace(strings.ReplaceAll(string(b), "\x00", " "))
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}

func argv0(args string) string {
	if i := strings.IndexByte(args, ' '); i >= 0 {
		return args[:i]
	}
	return args
}
