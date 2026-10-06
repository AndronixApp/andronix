package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/ui"
)

func TestOnOffAndID(t *testing.T) {
	Init(t.TempDir(), "2.0.0")
	t.Setenv("ANDRONIX_NO_TELEMETRY", "")
	t.Setenv("DO_NOT_TRACK", "")
	if _, err := os.Stat("/etc/andronix-release"); err == nil {
		t.Skip("inside a distro")
	}
	if !Enabled() {
		t.Fatal("on by default")
	}
	a, b := id(), id()
	if len(a) != 32 || a != b {
		t.Errorf("id %q %q", a, b)
	}
	SetEnabled(false)
	if Enabled() || id() != a {
		t.Error("off, and the id is kept")
	}
	SetEnabled(true)
	t.Setenv("ANDRONIX_NO_TELEMETRY", "1")
	if Enabled() {
		t.Error("ANDRONIX_NO_TELEMETRY")
	}
	t.Setenv("ANDRONIX_NO_TELEMETRY", "")
	t.Setenv("DO_NOT_TRACK", "1")
	if Enabled() {
		t.Error("DO_NOT_TRACK")
	}
}

func TestEventAndPost(t *testing.T) {
	Init(t.TempDir(), "2.0.0")
	t.Setenv("TERMUX_APK_RELEASE", "GOOGLE_PLAY_STORE")
	e := build("install_result", map[string]any{"ok": false, "distro": "debian", "step": "Downloading Debian 13"})
	if e.Event != "install_result" || len(e.InstallID) != 32 || e.Props["version"] != "2.0.0" ||
		e.Props["termux_flavor"] != "google_play_store" || e.Props["distro"] != "debian" || e.Props["arch"] == "" {
		t.Errorf("event: %+v", e)
	}
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		json.Unmarshal(b, &got)
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" || !strings.HasPrefix(r.UserAgent(), "andronix/") {
			t.Errorf("request: %s %v", r.Method, r.Header)
		}
	}))
	defer srv.Close()
	t.Setenv("ANDRONIX_TELEMETRY_URL", srv.URL)
	b, _ := json.Marshal(e)
	if err := Post(string(b)); err != nil || got["event"] != "install_result" {
		t.Errorf("post: %v %v", err, got)
	}
	// Nothing personal: no paths in the payload.
	if strings.Contains(string(b), os.Getenv("HOME")) || strings.Contains(string(b), filepath.Dir(stateFile())) {
		t.Errorf("payload has a path: %s", b)
	}
}

func TestErrorClass(t *testing.T) {
	for err, want := range map[error]string{
		nil:              "",
		ui.ErrCancelled:  "cancelled",
		context.Canceled: "cancelled",
		&ui.UserError{Title: "x", Class: "download"}: "download",
		ui.Errorf("Not enough space", "", ""):        "not_enough_space",
		errors.New("boom"):                           "other",
	} {
		if got := ErrorClass(err); got != want {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}
}

func TestHostAndTermuxPackage(t *testing.T) {
	t.Setenv("ANDRONIX_DISTRO", "")
	t.Setenv("ANDRONIX_HOST", "standalone")
	if h := host(); h != "standalone" {
		t.Errorf("host %q", h)
	}
	if f := termuxFlavor(); f != "" {
		t.Errorf("standalone counted as a Termux flavor: %q", f)
	}
	t.Setenv("ANDRONIX_HOST", "weird")
	if h := host(); h != "termux" && h != "linux" {
		t.Errorf("host outside the enum: %q", h)
	}
	t.Setenv("ANDRONIX_HOST", "")
	t.Setenv("ANDRONIX_DISTRO", "debian")
	if h := host(); h != "distro" {
		t.Errorf("host %q", h)
	}
	if got := token("googleplay.2026.02.11 (beta)"); got != "googleplay.2026.02.11_beta_" {
		t.Errorf("token %q", got)
	}
	t.Setenv("TERMUX_APP__PACKAGE_NAME", "com.termux.zerotermux")
	if got := termuxPackage(); got != "com.termux.zerotermux" {
		t.Errorf("pkg %q", got)
	}
}
