package app

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// #290: in the app, "done" goes out without waiting for the install_result
// send (a slow fork+exec on linker64 phones), and only once.
func TestDoneBeforeSend(t *testing.T) {
	old := ui.JSON
	ui.JSON = true
	var mu sync.Mutex
	var events []string
	gotDone := make(chan struct{}, 1)
	ui.SetEventLog(func(s string) {
		mu.Lock()
		events = append(events, s)
		mu.Unlock()
		if strings.Contains(s, `"t":"done"`) {
			gotDone <- struct{}{}
		}
	})
	defer func() { ui.JSON = old; ui.SetEventLog(nil) }()

	sendDone := false
	doneBefore(func() {
		select {
		case <-gotDone: // done was emitted while the send was still running
		case <-time.After(5 * time.Second):
			t.Error("done waited for the send")
		}
		sendDone = true
	})
	if !sendDone {
		t.Error("doneBefore returned before the send finished")
	}
	ui.EmitDone(nil) // main's closing call: a no-op now
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for _, e := range events {
		if strings.Contains(e, `"t":"done"`) {
			n++
			if !strings.Contains(e, `"status":"ok"`) {
				t.Errorf("done: %s", e)
			}
		}
	}
	if n != 1 {
		t.Errorf("%d done events, want 1: %q", n, events)
	}
}
