package ui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestStepRunsWaitsForARunningStep(t *testing.T) {
	var r stepRuns
	if !r.begin() {
		t.Fatal("begin refused before wait")
	}
	go func() {
		time.Sleep(100 * time.Millisecond)
		r.end()
	}()
	start := time.Now()
	if !r.wait(5 * time.Second) {
		t.Fatal("wait gave up on a step that ended")
	}
	if took := time.Since(start); took < 50*time.Millisecond {
		t.Fatalf("wait came back after %v, before the step ended", took)
	}
	// Nothing starts once the list is over.
	if r.begin() {
		t.Fatal("begin allowed after wait")
	}
}

func TestStepRunsWaitGivesUp(t *testing.T) {
	var r stepRuns
	r.begin()
	start := time.Now()
	if r.wait(50 * time.Millisecond) {
		t.Fatal("wait reported a step that never ended as ended")
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("wait took %v", took)
	}
}

// signalList runs the step list with one step that, once cancelled, takes
// a moment to stop what it started (as proot.Run does), sends sig to this
// process while it runs, and returns what the list returned and whether the
// step had finished stopping by then.
func signalList(t *testing.T, sig syscall.Signal) (error, bool) {
	t.Helper()
	oldI := Interactive
	Interactive = false // no keys: the signal is all there is
	t.Cleanup(func() { Interactive = oldI })
	// The list draws on os.Stdout; keep it out of the test output.
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	oldOut := os.Stdout
	os.Stdout = null
	t.Cleanup(func() { os.Stdout = oldOut; null.Close() })

	started := make(chan struct{})
	var stopped atomic.Bool
	steps := []Step{
		{Label: "Refreshing package lists", Run: func(ctx context.Context, r Reporter) error {
			close(started)
			<-ctx.Done()
			time.Sleep(300 * time.Millisecond) // ending its processes
			stopped.Store(true)
			return ctx.Err()
		}},
		{Label: "Never reached", Run: func(ctx context.Context, r Reporter) error {
			t.Error("the second step ran")
			return nil
		}},
	}
	// As in main: the signal also cancels the context the steps get. It is
	// also what keeps the signal from ending this test binary should it
	// arrive before bubbletea listens for it.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runTea(ctx, steps, nopLog{}) }()
	select {
	case <-started:
	case <-time.After(10 * time.Second):
		t.Fatal("the step never started")
	}
	time.Sleep(50 * time.Millisecond) // bubbletea's own handler is up by now
	if err := syscall.Kill(os.Getpid(), sig); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		return err, stopped.Load()
	case <-time.After(20 * time.Second):
		t.Fatal("the list didn't end after the signal")
	}
	return nil, false
}

// cancelled: the list's result reads as a cancel in main ("Stopped. Run the
// same command again", exit 130). ErrCancelled when bubbletea saw the
// signal; the context's own error on the rare run where only the step did.
func cancelled(err error) bool {
	return errors.Is(err, ErrCancelled) || errors.Is(err, context.Canceled)
}

// Ctrl-C in a terminal while the list takes no keys (--yes) arrives as
// SIGINT. The list returns a cancel, and only once the step has stopped
// what it started: an andronix that exits first leaves proot and its
// programs running (2.1.6 gates, bug 1).
func TestListInterruptedIsACancelAndWaitsForTheStep(t *testing.T) {
	err, stopped := signalList(t, syscall.SIGINT)
	if !cancelled(err) {
		t.Fatalf("runTea = %v, want a cancel", err)
	}
	if !stopped {
		t.Fatal("runTea returned before the cancelled step had ended")
	}
}

// SIGTERM ends the list without an error from bubbletea. With steps
// undone that is a cancel too, never a finished install.
func TestListTerminatedIsACancel(t *testing.T) {
	err, stopped := signalList(t, syscall.SIGTERM)
	if !cancelled(err) {
		t.Fatalf("runTea = %v, want a cancel", err)
	}
	if !stopped {
		t.Fatal("runTea returned before the cancelled step had ended")
	}
}

// What the list returns for every way it can end.
func TestListResult(t *testing.T) {
	boom := errors.New("apt stopped")
	other := errors.New("could not open a new TTY")
	interrupted := fmt.Errorf("%w: %w", tea.ErrProgramKilled, tea.ErrInterrupted)
	for _, c := range []struct {
		name    string
		err     error // bubbletea's
		stepErr error
		undone  bool
		want    error
	}{
		{"all steps done", nil, nil, false, nil},
		{"a step failed", nil, boom, true, boom},
		{"the last step failed", nil, boom, false, boom},
		{"Ctrl-C as a key", nil, ErrCancelled, true, ErrCancelled},
		{"SIGINT without keys", interrupted, nil, true, ErrCancelled},
		{"SIGINT after a step failed", interrupted, boom, true, ErrCancelled},
		{"SIGTERM with steps undone", nil, nil, true, ErrCancelled},
		{"bubbletea's own failure", other, nil, true, other},
	} {
		if got := listResult(c.err, c.stepErr, c.undone); got != c.want {
			t.Errorf("%s: listResult = %v, want %v", c.name, got, c.want)
		}
	}
}
