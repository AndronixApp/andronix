package proot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeProc is a /proc made of files: status and maps per pid.
type fakeProc struct {
	t   *testing.T
	dir string
}

func newFakeProc(t *testing.T) *fakeProc {
	t.Helper()
	old := procDir
	dir := t.TempDir()
	procDir = dir
	t.Cleanup(func() { procDir = old })
	os.WriteFile(filepath.Join(dir, "uptime"), []byte("1 1\n"), 0o644) // not a pid
	return &fakeProc{t: t, dir: dir}
}

// add makes a process: its state letter, tracer, and the lines of its maps.
func (f *fakeProc) add(pid int, state string, tracer int, maps ...string) {
	f.t.Helper()
	d := filepath.Join(f.dir, strconv.Itoa(pid))
	if err := os.MkdirAll(d, 0o755); err != nil {
		f.t.Fatal(err)
	}
	st := fmt.Sprintf("Name:\tx\nUmask:\t0077\nState:\t%s (x)\nTgid:\t%d\nPid:\t%d\nPPid:\t1\nTracerPid:\t%d\nUid:\t1\n", state, pid, pid, tracer)
	os.WriteFile(filepath.Join(d, "status"), []byte(st), 0o644)
	os.WriteFile(filepath.Join(d, "maps"), []byte(strings.Join(maps, "\n")+"\n"), 0o644)
}

func (f *fakeProc) remove(pid int) { os.RemoveAll(filepath.Join(f.dir, strconv.Itoa(pid))) }

type sent struct {
	pid int
	sig syscall.Signal
}

// recordKills replaces killProc; a SIGKILL removes the process from f.
func recordKills(t *testing.T, f *fakeProc, onKill func(pid int)) *[]sent {
	t.Helper()
	old, oldPause := killProc, sweepPause
	var got []sent
	killProc = func(pid int, sig syscall.Signal) error {
		got = append(got, sent{pid, sig})
		if sig == syscall.SIGKILL {
			if _, err := os.Stat(filepath.Join(f.dir, strconv.Itoa(pid))); err != nil {
				return syscall.ESRCH
			}
			f.remove(pid)
			if onKill != nil {
				onKill(pid)
			}
		}
		return nil
	}
	sweepPause = 0
	t.Cleanup(func() { killProc, sweepPause = old, oldPause })
	return &got
}

func mapsOf(file string) string {
	return "7f0000000000-7f0000001000 r-xp 00000000 fd:01 1234                       " + file
}

func TestTracees(t *testing.T) {
	f := newFakeProc(t)
	f.add(100, "S", 0)   // proot
	f.add(101, "S", 100) // its shell
	f.add(102, "t", 100) // stopped in a ptrace stop
	f.add(103, "Z", 100) // a main thread that exited; its threads may live
	f.add(104, "S", 200) // another proot's
	f.add(105, "S", 0)   // nobody's
	got := Tracees(100)
	sort.Ints(got)
	if want := []int{101, 102, 103}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Tracees(100) = %v, want %v", got, want)
	}
	// "No tracer" is not a tracer: that would be every untraced process.
	if got := Tracees(0); len(got) != 0 {
		t.Fatalf("Tracees(0) = %v", got)
	}
}

// prootSignals records the signals stopSession sends to proot itself.
func prootSignals(got *[]sent, pid int, fail error) func(syscall.Signal) error {
	return func(sig syscall.Signal) error {
		*got = append(*got, sent{pid, sig})
		return fail
	}
}

// A cancelled run: proot is frozen, everything it traces is killed (the
// gpg-agent in its own session too, and a process born during the sweep),
// and proot goes last. Other proots' processes are left alone.
func TestStopSessionKillsTraceesThenProot(t *testing.T) {
	f := newFakeProc(t)
	f.add(100, "S", 0)   // proot
	f.add(101, "S", 100) // sh -c
	f.add(102, "S", 100) // pacman-key
	f.add(103, "S", 100) // gpg-agent --daemon (own session)
	f.add(200, "S", 0)   // another distro's proot
	f.add(201, "S", 200) // and its shell
	born := false
	got := recordKills(t, f, func(pid int) {
		if pid == 102 && !born {
			born = true
			f.add(110, "t", 100) // forked right before it died
		}
	})
	if err := stopSession(100, prootSignals(got, 100, nil)); err != nil {
		t.Fatalf("stopSession = %v", err)
	}

	if len(*got) < 2 || (*got)[0] != (sent{100, syscall.SIGSTOP}) {
		t.Fatalf("proot isn't frozen first: %v", *got)
	}
	if last := (*got)[len(*got)-1]; last != (sent{100, syscall.SIGKILL}) {
		t.Fatalf("proot isn't killed last: %v", *got)
	}
	killed := map[int]int{}
	for _, s := range (*got)[1 : len(*got)-1] {
		if s.sig != syscall.SIGKILL {
			t.Fatalf("unexpected signal %v", s)
		}
		killed[s.pid]++
	}
	for _, pid := range []int{101, 102, 103, 110} {
		if killed[pid] != 1 {
			t.Errorf("tracee %d was killed %d times, want once: %v", pid, killed[pid], *got)
		}
	}
	for _, pid := range []int{200, 201} {
		if killed[pid] != 0 {
			t.Errorf("process %d isn't this proot's and was killed", pid)
		}
	}
}

// proot ended by itself before the cancel got to it: nothing is signalled
// (its pid may be someone else's by now) and the caller hears it.
func TestStopSessionProotAlreadyDone(t *testing.T) {
	f := newFakeProc(t)
	f.add(101, "S", 100)
	got := recordKills(t, f, nil)
	err := stopSession(100, prootSignals(got, 100, os.ErrProcessDone))
	if !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("stopSession = %v, want os.ErrProcessDone", err)
	}
	if want := []sent{{100, syscall.SIGSTOP}}; !reflect.DeepEqual(*got, want) {
		t.Fatalf("signals = %v, want %v", *got, want)
	}
}

func TestStopSessionRefusesInit(t *testing.T) {
	f := newFakeProc(t)
	got := recordKills(t, f, nil)
	for _, pid := range []int{0, 1} {
		if err := stopSession(pid, prootSignals(got, pid, nil)); !errors.Is(err, os.ErrProcessDone) {
			t.Fatalf("stopSession(%d) = %v", pid, err)
		}
	}
	if len(*got) != 0 {
		t.Fatalf("signals sent: %v", *got)
	}
}

// What a killed proot leaves: the distro's processes without a tracer.
// Only those go; a traced one (a live session), Termux's own gpg-agent,
// another distro's leftovers and a program that merely reads a file of the
// rootfs stay.
func TestKillOrphans(t *testing.T) {
	f := newFakeProc(t)
	home := t.TempDir()
	rootfs := filepath.Join(home, "distros", "arch", "rootfs")
	other := filepath.Join(home, "distros", "manjaro", "rootfs")
	os.MkdirAll(rootfs, 0o755)
	// The same folder under another name, as /data/user/0 is for /data/data.
	link := filepath.Join(home, "link")
	if err := os.Symlink(filepath.Join(home, "distros"), link); err != nil {
		t.Skip("no symlinks here")
	}
	real, _ := filepath.EvalSymlinks(rootfs)
	loader := "/data/data/com.termux/files/usr/libexec/proot/loader"

	f.add(300, "S", 0, mapsOf(loader), mapsOf(real+"/usr/bin/gpg-agent"), mapsOf(real+"/usr/lib/libc.so.6")) // the leftover agent
	f.add(301, "R", 0, mapsOf(real+"/usr/bin/bash"))                                                         // the shell spinning in wait()
	f.add(302, "S", 500, mapsOf(real+"/usr/bin/gpg-agent"))                                                  // under a live proot
	f.add(303, "S", 0, mapsOf("/data/data/com.termux/files/usr/bin/gpg-agent"))                              // Termux's own
	f.add(304, "S", 0, mapsOf(other+"/usr/bin/gpg-agent"))                                                   // another distro's
	f.add(305, "Z", 0)                                                                                       // a zombie has no mappings
	f.add(306, "S", 0, strings.Replace(mapsOf(real+"/var/lib/pacman/sync/core.db"), "r-xp", "r--p", 1))      // reads a file
	f.add(307, "S", 0, mapsOf(real+"-old/usr/bin/gpg-agent"))                                                // a folder next to it
	f.add(308, "S", 0, mapsOf(real+"/usr/bin/a b (deleted)"))                                                // a name with spaces
	f.add(os.Getpid(), "S", 0, mapsOf(real+"/usr/local/bin/andronix"))
	got := recordKills(t, f, nil)

	// Asked through the symlinked name, found under the real one.
	ps := killOrphans(filepath.Join(link, "arch", "rootfs"))
	sort.Ints(ps)
	if want := []int{300, 301, 308}; !reflect.DeepEqual(ps, want) {
		t.Fatalf("killOrphans = %v, want %v (signals %v)", ps, want, *got)
	}
	for _, s := range *got {
		if s.sig != syscall.SIGKILL {
			t.Errorf("unexpected signal %v", s)
		}
	}
	// Nothing left to do the second time.
	if ps := killOrphans(rootfs); len(ps) != 0 {
		t.Fatalf("second killOrphans = %v", ps)
	}
}

func TestKillOrphansNeedsARootfs(t *testing.T) {
	f := newFakeProc(t)
	f.add(300, "S", 0, mapsOf("/usr/bin/gpg-agent"))
	got := recordKills(t, f, nil)
	for _, r := range []string{"", "/", "."} {
		if ps := killOrphans(r); len(ps) != 0 || len(*got) != 0 {
			t.Fatalf("killOrphans(%q) = %v, signals %v", r, ps, *got)
		}
	}
}

func TestReapOrphansOncePerRootfs(t *testing.T) {
	f := newFakeProc(t)
	rootfs := filepath.Join(t.TempDir(), "rootfs")
	os.MkdirAll(rootfs, 0o755)
	real, _ := filepath.EvalSymlinks(rootfs)
	t.Setenv("ANDRONIX_HOME", t.TempDir()) // the log line's home
	got := recordKills(t, f, nil)
	tg := &Target{Rootfs: rootfs}

	f.add(300, "S", 0, mapsOf(real+"/usr/bin/gpg-agent"))
	tg.reapOrphans()
	if len(*got) != 1 {
		t.Fatalf("first reap: %v", *got)
	}
	f.add(301, "S", 0, mapsOf(real+"/usr/bin/gpg-agent"))
	tg.reapOrphans()
	if len(*got) != 1 {
		t.Fatalf("second reap looked again: %v", *got)
	}
	// Inside the distro (Local) there's no proot of ours to have been killed.
	(&Target{Rootfs: "/", Local: true}).reapOrphans()
	if len(*got) != 1 {
		t.Fatalf("local reap: %v", *got)
	}
}

func shortWaitDelay(t *testing.T) {
	t.Helper()
	old, oldMax := waitDelay, drainMax
	waitDelay, drainMax = 300*time.Millisecond, 2*time.Second
	t.Cleanup(func() { waitDelay, drainMax = old, oldMax })
}

func needSh(t *testing.T) {
	t.Helper()
	if _, err := os.Stat("/bin/sh"); err != nil {
		t.Skip("no /bin/sh")
	}
}

// killLater ends the background process whose pid the command printed.
func killLater(t *testing.T, line string) {
	t.Helper()
	if pid, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && pid > 1 {
		t.Cleanup(func() { syscall.Kill(pid, syscall.SIGKILL) })
	}
}

// Cancel with a process left holding the output pipe (2.1.6 gates, bug 1:
// the guest's shell outlived its killed proot and the installer waited for
// the pipe for good). Run comes back, with ctx's error.
func TestRunCancelDoesNotWaitForThePipe(t *testing.T) {
	needSh(t)
	shortWaitDelay(t)
	t.Setenv("ANDRONIX_HOME", t.TempDir()) // the log line's home
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tg := &Target{Local: true}
	started := make(chan string, 1)
	errc := make(chan error, 1)
	go func() {
		// The shell is killed by the cancel; its sleep keeps the pipe.
		errc <- tg.Run(ctx, "sleep 60 & echo $!; wait", nil, func(l string) {
			select {
			case started <- l:
			default:
			}
		})
	}()
	select {
	case l := <-started:
		killLater(t, l)
	case err := <-errc:
		t.Fatalf("Run ended early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the command never started")
	}
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run still waits for the pipe 10 s after the cancel")
	}
}

// A command that ended well but left a silent process with the pipe: Run
// returns its success, and the lines printed before.
func TestRunDoesNotWaitForALeftoverProcess(t *testing.T) {
	needSh(t)
	shortWaitDelay(t)
	t.Setenv("ANDRONIX_HOME", t.TempDir())
	tg := &Target{Local: true}
	var lines []string
	start := time.Now()
	err := tg.Run(context.Background(), "sleep 60 & echo $!; echo done", nil, func(l string) { lines = append(lines, l) })
	if len(lines) > 0 {
		killLater(t, lines[0])
	}
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("Run took %v", took)
	}
	if len(lines) != 2 || lines[1] != "done" {
		t.Fatalf("lines = %q", lines)
	}
}

// The leftover process keeps writing: the pipe is never idle, and Run
// still comes back (drainMax).
func TestRunDoesNotWaitForAChattyLeftover(t *testing.T) {
	needSh(t)
	shortWaitDelay(t)
	t.Setenv("ANDRONIX_HOME", t.TempDir())
	var first string
	start := time.Now()
	err := (&Target{Local: true}).Run(context.Background(),
		"( while :; do echo x; sleep 0.05; done ) & echo $!", nil, func(l string) {
			if first == "" {
				first = l
			}
		})
	killLater(t, first)
	if err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if took := time.Since(start); took > 15*time.Second {
		t.Fatalf("Run took %v", took)
	}
}

// The leftover process writes faster than we read: no read ever waits, so
// neither time limit is reached, and Run still comes back (drainBytes).
func TestRunDoesNotWaitForAFloodingLeftover(t *testing.T) {
	needSh(t)
	shortWaitDelay(t)
	old := drainBytes
	drainBytes = 256 << 10
	t.Cleanup(func() { drainBytes = old })
	t.Setenv("ANDRONIX_HOME", t.TempDir())
	var first string
	errc := make(chan error, 1)
	go func() {
		errc <- (&Target{Local: true}).Run(context.Background(),
			"( while :; do echo xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx; done ) & echo $!", nil, func(l string) {
				if first == "" {
					first = l
					killLater(t, first)
				}
			})
	}()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Run = %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run still reads the flood after 30 s")
	}
}

// Output that is in the pipe when the command ends is all read, however
// slowly the caller takes it: the wait is for a silent pipe, not a busy one.
func TestRunReadsAllOutputWithASlowReader(t *testing.T) {
	needSh(t)
	old, oldMax := waitDelay, drainMax
	waitDelay, drainMax = 50*time.Millisecond, 30*time.Second
	t.Cleanup(func() { waitDelay, drainMax = old, oldMax })
	t.Setenv("ANDRONIX_HOME", t.TempDir())
	const n = 1500
	var lines []string
	err := (&Target{Local: true}).Run(context.Background(),
		fmt.Sprintf("i=0; while [ $i -lt %d ]; do echo line$i; i=$((i+1)); done; echo RESULT=ok", n), nil,
		func(l string) {
			lines = append(lines, l)
			time.Sleep(200 * time.Microsecond) // well past waitDelay in sum
		})
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if len(lines) != n+1 || lines[n] != "RESULT=ok" {
		last := ""
		if len(lines) > 0 {
			last = lines[len(lines)-1]
		}
		t.Fatalf("%d lines, last %q; want %d ending in RESULT=ok", len(lines), last, n+1)
	}
}

// outPipe after the command's end: a silent pipe that is still open reads
// as ended after waitDelay, and stays ended (the next read doesn't wait
// again); what was written before is read first.
func TestOutPipeGivesUpOnceAndStaysEnded(t *testing.T) {
	old, oldMax := waitDelay, drainMax
	waitDelay, drainMax = 400*time.Millisecond, 30*time.Second
	t.Cleanup(func() { waitDelay, drainMax = old, oldMax })
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	defer pw.Close() // the leftover process's end stays open
	pw.Write([]byte("tail\n"))
	o := &outPipe{f: pr}
	o.ended()
	buf := make([]byte, 64)
	if n, err := o.Read(buf); err != nil || string(buf[:n]) != "tail\n" {
		t.Fatalf("first read = %q, %v", buf[:n], err)
	}
	if n, err := o.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("read of the silent pipe = %d, %v; want EOF", n, err)
	}
	if !o.keptOpen() {
		t.Fatal("keptOpen is false")
	}
	start := time.Now()
	if n, err := o.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("read after giving up = %d, %v; want EOF", n, err)
	}
	if took := time.Since(start); took > waitDelay/2 {
		t.Fatalf("the read after giving up waited again (%v)", took)
	}
}

// While the command runs nothing bounds a read; a real end of the pipe is
// an end, not a pipe kept open.
func TestOutPipeWaitsWhileTheCommandRuns(t *testing.T) {
	old, oldMax := waitDelay, drainMax
	waitDelay, drainMax = 50*time.Millisecond, 100*time.Millisecond
	t.Cleanup(func() { waitDelay, drainMax = old, oldMax })
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pr.Close()
	o := &outPipe{f: pr}
	go func() {
		time.Sleep(4 * waitDelay) // silent for longer than both limits
		pw.Write([]byte("late\n"))
		pw.Close()
	}()
	buf := make([]byte, 64)
	if n, err := o.Read(buf); err != nil || string(buf[:n]) != "late\n" {
		t.Fatalf("read = %q, %v", buf[:n], err)
	}
	o.ended()
	if n, err := o.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("read at the end = %d, %v", n, err)
	}
	if o.keptOpen() {
		t.Fatal("a closed pipe counts as kept open")
	}
}

// A deadline isn't a cancel: the caller sees how the command ended.
func TestRunDeadlineKeepsTheExitError(t *testing.T) {
	needSh(t)
	shortWaitDelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	err := (&Target{Local: true}).Run(ctx, "exec sleep 60", nil, nil)
	if err == nil || errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the command's own error", err)
	}
	if !signaled(err) {
		t.Fatalf("Run = %v, want a command ended by a signal", err)
	}
}

// fakeProot makes PROOT_BIN a script that runs the command (its last
// argument) as proot would, in its own process.
func fakeProot(t *testing.T) *Target {
	t.Helper()
	needSh(t)
	dir := t.TempDir()
	bin := filepath.Join(dir, "proot")
	script := "#!/bin/sh\n[ \"$1\" = --help ] && exit 0\nfor a; do last=$a; done\nexec /bin/sh -c \"$last\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PROOT_BIN", bin)
	t.Setenv("ANDRONIX_HOME", filepath.Join(dir, "home"))
	rootfs := filepath.Join(dir, "rootfs")
	os.MkdirAll(rootfs, 0o755)
	return &Target{ID: "arch", Dir: dir, Rootfs: rootfs}
}

// The whole of a cancelled run under proot (2.1.6 gates, bugs 1 and 2):
// what an earlier killed run left is ended before the command starts; the
// cancel freezes proot, kills what it traces, then proot; Run returns
// ctx's error; and what the kill itself left is swept right after.
func TestRunUnderProotCancel(t *testing.T) {
	tg := fakeProot(t)
	shortWaitDelay(t)
	f := newFakeProc(t)
	real, _ := filepath.EvalSymlinks(tg.Rootfs)
	f.add(300, "S", 0, mapsOf(real+"/usr/bin/gpg-agent")) // from an earlier, killed run
	got := recordKills(t, f, func(pid int) {
		if pid == 401 {
			// One that slipped out of the sweep, now without a tracer.
			f.add(500, "S", 0, mapsOf(real+"/usr/bin/bash"))
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan int, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- tg.Run(ctx, "echo $$; exec sleep 60", nil, func(l string) {
			if pid, err := strconv.Atoi(strings.TrimSpace(l)); err == nil {
				started <- pid
			}
		})
	}()
	var proot int
	select {
	case proot = <-started:
	case err := <-errc:
		t.Fatalf("Run ended early: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("the command never started")
	}
	t.Cleanup(func() { syscall.Kill(proot, syscall.SIGKILL) })
	if want := []sent{{300, syscall.SIGKILL}}; !reflect.DeepEqual(*got, want) {
		t.Fatalf("before the command: signals %v, want %v", *got, want)
	}
	f.add(400, "S", proot) // the guest's shell
	f.add(401, "S", proot) // gpg-agent, in its own session

	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run didn't come back within 10 s of the cancel")
	}
	killed := map[int]bool{}
	for _, s := range *got {
		killed[s.pid] = true
	}
	for _, pid := range []int{400, 401, 500} {
		if !killed[pid] {
			t.Errorf("process %d wasn't ended: %v", pid, *got)
		}
	}
	// proot itself went through the process handle, and is gone.
	if err := syscall.Kill(proot, 0); err == nil {
		t.Errorf("proot (%d) is still there", proot)
	}
}

// A run that ends by itself sweeps nothing afterwards, and a later run in
// the same distro doesn't look for an earlier run's leftovers again.
func TestRunUnderProotCleanEnd(t *testing.T) {
	tg := fakeProot(t)
	shortWaitDelay(t)
	f := newFakeProc(t)
	real, _ := filepath.EvalSymlinks(tg.Rootfs)
	got := recordKills(t, f, nil)
	var lines []string
	if err := tg.Run(context.Background(), "echo one", nil, func(l string) { lines = append(lines, l) }); err != nil {
		t.Fatalf("Run = %v", err)
	}
	f.add(300, "S", 0, mapsOf(real+"/usr/bin/gpg-agent"))
	if err := tg.Run(context.Background(), "echo two; exit 3", nil, func(l string) { lines = append(lines, l) }); err == nil || signaled(err) {
		t.Fatalf("Run = %v, want exit status 3", err)
	}
	if !reflect.DeepEqual(lines, []string{"one", "two"}) {
		t.Fatalf("lines = %q", lines)
	}
	if len(*got) != 0 {
		t.Fatalf("signals after clean runs: %v", *got)
	}
}

func TestFirstLine(t *testing.T) {
	if got := firstLine("  a b\nc"); got != "a b" {
		t.Fatalf("firstLine = %q", got)
	}
	if got := firstLine(strings.Repeat("x", 200)); len(got) != 80 {
		t.Fatalf("firstLine keeps %d bytes", len(got))
	}
}
