package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Nothing that identifies the user or unlocks a purchase leaves the phone.
func TestRedact(t *testing.T) {
	r := &redactor{home: "/data/data/com.termux/files/home", users: []string{"alex"}}
	in := strings.Join([]string{
		"andronix install --edition ubuntu-xfce --token 'k=KEY123&e=1790000000&h=abc%2Bdef'",
		"GET https://products.andronix.xyz/v1/modded/download/ubuntu.tar.xz?e=1&h=x&k=KEY123 200",
		"token k=KEY9&e=17&h=HASH",
		"mail jane.doe@example.com failed",
		"/data/data/com.termux/files/home/.andronix/logs/install.log and /data/user/0/com.termux/files/home/x",
		"uid u0_a214 ran as alex in /home/alex; alexander stays",
		`Authorization: Bearer abc.def`,
	}, "\n")
	out := r.do(in)
	for _, bad := range []string{"KEY123", "KEY9", "abc%2Bdef", "HASH", "jane.doe", "example.com", "com.termux/files/home", "u0_a214", "/home/alex", "abc.def"} {
		if strings.Contains(out, bad) {
			t.Errorf("%q left in:\n%s", bad, out)
		}
	}
	for _, keep := range []string{"--token <token>", "ubuntu.tar.xz?<redacted>", "~/.andronix/logs/install.log", "alexander stays", "<uid>", "<email>"} {
		if !strings.Contains(out, keep) {
			t.Errorf("want %q in:\n%s", keep, out)
		}
	}
}

func TestReportLocale(t *testing.T) {
	for v, want := range map[string]string{"de_DE.UTF-8": "de-DE", "pt_BR": "pt-BR", "de": "de", "C.UTF-8": "", "POSIX": "", "en_US@euro": "en-US", "fil_PH.UTF-8": "fil-PH", "sr_RS_latin": "sr", "": "", "_": "", "..": ""} {
		t.Setenv("LC_ALL", "")
		t.Setenv("LC_MESSAGES", "")
		t.Setenv("LANG", v)
		if got := reportLocale(); got != want {
			t.Errorf("LANG=%q: got %q, want %q", v, got, want)
		}
	}
}

func TestTail(t *testing.T) {
	var b strings.Builder
	for i := 0; i < 1000; i++ {
		b.WriteString(strings.Repeat("x", 99) + "\n")
	}
	if got := tail(b.String(), 400, 48<<10); strings.Count(got, "\n") != 399 {
		t.Errorf("400 lines: got %d", strings.Count(got, "\n")+1)
	}
	if got := tail(b.String(), 1000, 1000); len(got) > 1000 || strings.HasPrefix(got, "\n") {
		t.Errorf("byte cap: %d", len(got))
	}
}

// A failed command is recorded; report builds from it, redacted.
func TestReportFromFailure(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANDRONIX_HOME", "")
	logs := filepath.Join(home, ".andronix/logs")
	os.MkdirAll(logs, 0o755)
	lg := filepath.Join(logs, "install-kali-20260927-010101.log")
	os.WriteFile(lg, []byte("step one\nfetching https://dl.andronix.app/x.tar.xz?sig=SECRET\nE: basic.conf: Protocol driver not attached\n"), 0o644)
	ui.FailedStep = "Installing XFCE desktop"
	SaveFailure("install", "kali", &ui.UserError{Title: "Couldn't install xfce desktop", What: "The package manager stopped with an error.", Log: lg})
	r := buildReport("desktop never installs")
	in := r.Install
	if in == nil || in.Command != "install" || in.Status != "fail" || in.Distro != "kali" || in.Step != "Installing XFCE desktop" || in.Error == nil || in.At == 0 || r.LogName != filepath.Base(lg) {
		t.Fatalf("report: %+v %+v", r, in)
	}
	if strings.Contains(r.Log, "SECRET") || !strings.Contains(r.Log, "Protocol driver not attached") {
		t.Errorf("log: %q", r.Log)
	}
	if r.Schema != 1 || r.Source != "installer" || !r.Consent || !uuidRe.MatchString(r.ClientID) || r.Versions.Installer != Version || r.Device.Arch == "" {
		t.Errorf("header: %+v", r)
	}
	// A retry reuses the client_id until the server has the report.
	if again := buildReport("x"); again.ClientID != r.ClientID {
		t.Errorf("client_id changed: %s %s", r.ClientID, again.ClientID)
	}
	// Only contract keys at the top level.
	b, _ := json.Marshal(r)
	var top map[string]any
	json.Unmarshal(b, &top)
	allowed := map[string]bool{"schema": true, "source": true, "client_id": true, "consent": true, "created_at": true, "description": true,
		"install_id": true, "locale": true, "versions": true, "device": true, "termux": true, "install": true, "compat": true, "log": true, "log_name": true}
	for k := range top {
		if !allowed[k] {
			t.Errorf("key %q isn't in reports-v1", k)
		}
	}
	// The report command itself failing isn't recorded over it.
	SaveFailure("report", "", &ui.UserError{Title: "x"})
	if f := loadFailure(); f == nil || f.Command != "install" {
		t.Errorf("failure overwritten: %+v", f)
	}
}

// The draft flow (reports-v1 draft 3) without the app: upload the draft
// (a 503 first: the retry keeps client_id), then "send now" finalizes it
// with the claim token in a header and the user's line.
func TestReportDraftFlow(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANDRONIX_HOME", "")
	t.Setenv("ANDRONIX_DISTRO", "")
	ui.Yes = true
	defer func() { ui.Yes = false }()
	var clientIDs []string
	var fin map[string]any
	var claim string
	drafts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" || r.Method != http.MethodPost {
			w.WriteHeader(415)
			return
		}
		b, _ := io.ReadAll(r.Body)
		switch {
		case r.URL.Path == "/v1/reports" && r.URL.Query().Get("draft") == "1":
			var body map[string]any
			json.Unmarshal(b, &body)
			clientIDs = append(clientIDs, body["client_id"].(string))
			if drafts++; drafts == 1 {
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(201)
			w.Write([]byte(`{"id":"AX-7K3Q9M","claim_token":"SECRETTOKEN","expires_at":"2099-01-01T00:00:00Z","link":"andronix://report/draft/AX-7K3Q9M?t=SECRETTOKEN"}`))
		case r.URL.Path == "/v1/reports/drafts/AX-7K3Q9M/finalize":
			claim = r.Header.Get("X-Report-Claim")
			json.Unmarshal(b, &fin)
			w.Write([]byte(`{"id":"AX-7K3Q9M","received_at":"2026-09-27T10:00:00Z"}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer srv.Close()
	t.Setenv("ANDRONIX_API", srv.URL)
	if err := Report(context.Background(), false, "desktop stays black", ""); err != nil {
		t.Fatal(err)
	}
	if len(clientIDs) != 2 || clientIDs[0] != clientIDs[1] {
		t.Errorf("draft retries must reuse client_id: %v", clientIDs)
	}
	if claim != "SECRETTOKEN" || fin["consent"] != true || fin["description"] != "desktop stays black" || fin["client_id"] == clientIDs[0] {
		t.Errorf("finalize: claim %q body %v", claim, fin)
	}
	if _, err := os.Stat(filepath.Join(home, ".andronix/report-drafts/AX-7K3Q9M.json")); err == nil {
		t.Error("the draft (with its claim token) was kept after it was sent")
	}
}

func TestDraftFilesAndOpen(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANDRONIX_HOME", "")
	t.Setenv("ANDRONIX_DISTRO", "")
	d := &reportDraft{ID: "AX-7K3Q9M", ClaimToken: "tok", ExpiresAt: "2099-01-01T00:00:00Z", Link: "andronix://report/draft/AX-7K3Q9M?t=tok"}
	saveDraft(d)
	st, err := os.Stat(filepath.Join(home, ".andronix/report-drafts/AX-7K3Q9M.json"))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("draft file: %v %v", st, err)
	}
	removeDraft(d.ID)
	// One saved inside a distro (its /tmp) is found from Termux for --open.
	dir := filepath.Join(home, ".andronix/distros/debian/rootfs/tmp/andronix-report-drafts")
	os.MkdirAll(dir, 0o700)
	b, _ := json.Marshal(d)
	os.WriteFile(filepath.Join(dir, "AX-7K3Q9M.json"), b, 0o600)
	if got := findDraft("AX-7K3Q9M"); got == nil || got.ClaimToken != "tok" {
		t.Errorf("findDraft: %+v", got)
	}
	// Off Android the app can't be opened: --open says so.
	if err := openDraft(context.Background(), "AX-7K3Q9M"); err == nil {
		t.Error("openDraft off Android: want an error")
	}
	if err := openDraft(context.Background(), "ax-nope"); err == nil {
		t.Error("bad id accepted")
	}
}

func TestReportErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("ANDRONIX_HOME", "")
	for _, c := range []struct {
		status int
		body   string
	}{{201, `{"id":"AX-7K2QIL","claim_token":"x","link":"andronix://report/draft/AX-7K2QIL?t=x"}`}, {201, `{"id":"AX-7K3Q9M","claim_token":"x","link":"https://evil/"}`},
		{429, `{"error":"rate_limited"}`}, {400, `{"error":"invalid_request","message":"bad","field":"device.sdk"}`}, {404, `{"error":"disabled"}`}} {
		c := c
		bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(c.status)
			w.Write([]byte(c.body))
		}))
		t.Setenv("ANDRONIX_API", bad.URL)
		if _, err := postDraft(context.Background(), &problemReport{}); err == nil {
			t.Errorf("%d %s: accepted", c.status, c.body)
		}
		bad.Close()
	}
	for _, st := range []int{410, 401} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(st) }))
		t.Setenv("ANDRONIX_API", srv.URL)
		if err := finalizeDraft(context.Background(), &reportDraft{ID: "AX-7K3Q9M", ClaimToken: "x"}, "x"); err == nil {
			t.Errorf("finalize %d: accepted", st)
		}
		srv.Close()
	}
}
