package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/AndronixApp/andronix-distros/internal/conf"
	"github.com/AndronixApp/andronix-distros/internal/netx"
	"github.com/AndronixApp/andronix-distros/internal/sys"
	"github.com/AndronixApp/andronix-distros/internal/ui"
)

// Problem reports (andronix report): what failed, this phone's facts and
// the end of the failing log, redacted on the phone, shown in full, and
// sent only when the user says so. The support desk's dashboard reads
// them; the reply is an AX-ID the user can quote.

// failure is the last command that failed (~/.andronix/last-error.json).
type failure struct {
	Time    string `json:"time"`
	Version string `json:"version"`
	Command string `json:"command"`
	Distro  string `json:"distro,omitempty"`
	Edition string `json:"edition,omitempty"`
	Step    string `json:"step,omitempty"`
	Title   string `json:"title"`
	What    string `json:"what"`
	Log     string `json:"log,omitempty"`
}

func failurePath() string { return filepath.Join(GetPaths().Home, "last-error.json") }

// SaveFailure records a failed command for andronix report. Best effort.
func SaveFailure(cmd, name string, err error) {
	if cmd == "report" || err == nil {
		return
	}
	f := failure{Time: time.Now().UTC().Format(time.RFC3339), Version: Version, Command: cmd, Step: ui.FailedStep}
	if d, derr := conf.ResolveDistro(name); derr == nil {
		f.Distro = d.ID
	} else if ed, eerr := conf.ResolveEdition(name); eerr == nil {
		f.Distro, f.Edition = ed.Distro, ed.ID
	}
	var ue *ui.UserError
	if errors.As(err, &ue) {
		f.Title, f.What, f.Log = ue.Title, ue.What, ue.Log
		if ue.Err != nil {
			f.What += ": " + ue.Err.Error()
		}
	} else {
		f.Title, f.What = "Something went wrong", err.Error()
	}
	b, _ := json.MarshalIndent(f, "", "  ")
	os.MkdirAll(GetPaths().Home, 0o755)
	os.WriteFile(failurePath(), b, 0o600)
}

func loadFailure() *failure {
	b, err := os.ReadFile(failurePath())
	if err != nil {
		return nil
	}
	var f failure
	if json.Unmarshal(b, &f) != nil {
		return nil
	}
	return &f
}

// problemReport is what andronix report sends: POST /v1/reports, schema 1
// (support-desk docs/contracts/reports-v1.md; support owns the schema).
type problemReport struct {
	Schema      int            `json:"schema"`
	Source      string         `json:"source"`
	ClientID    string         `json:"client_id"`
	Consent     bool           `json:"consent"`
	CreatedAt   int64          `json:"created_at"`
	Description string         `json:"description,omitempty"`
	InstallID   string         `json:"install_id,omitempty"`
	Locale      string         `json:"locale,omitempty"`
	Versions    reportVersions `json:"versions"`
	Device      reportDevice   `json:"device"`
	Termux      *reportTermux  `json:"termux,omitempty"`
	Install     *reportInstall `json:"install,omitempty"`
	Compat      *reportCompat  `json:"compat,omitempty"`
	Doctor      string         `json:"doctor,omitempty"`
	LogName     string         `json:"log_name,omitempty"`
	Log         string         `json:"log,omitempty"`
}

type reportVersions struct {
	Installer string `json:"installer"`
}

type reportDevice struct {
	SDK           int    `json:"sdk,omitempty"`
	Kernel        string `json:"kernel,omitempty"`
	ROM           string `json:"rom,omitempty"`
	Arch          string `json:"arch,omitempty"`
	RAMMB         int64  `json:"ram_mb,omitempty"`
	FreeStorageMB int64  `json:"free_storage_mb,omitempty"`
	Phantom       string `json:"phantom,omitempty"`
}

type reportTermux struct {
	Installed bool   `json:"installed"`
	Source    string `json:"source,omitempty"`
	Version   string `json:"version,omitempty"`
}

type reportInstall struct {
	Command string       `json:"command,omitempty"`
	Status  string       `json:"status,omitempty"`
	Distro  string       `json:"distro,omitempty"`
	Edition string       `json:"edition,omitempty"`
	DE      string       `json:"de,omitempty"`
	Modded  bool         `json:"modded,omitempty"`
	Step    string       `json:"step,omitempty"`
	At      int64        `json:"at,omitempty"`
	Error   *reportError `json:"error,omitempty"`
}

type reportError struct {
	Title  string `json:"title,omitempty"`
	Detail string `json:"detail,omitempty"`
}

type reportCompat struct {
	Probe map[string]string `json:"probe,omitempty"`
	Rules []string          `json:"rules,omitempty"`
}

const (
	reportLogLines = 400
	reportLogBytes = 60 << 10
	reportDescMax  = 300
)

// buildReport gathers the report; everything personal is redacted here.
func buildReport(desc string) *problemReport {
	f := loadFailure()
	var d *conf.Distro
	r := &problemReport{Schema: 1, Source: "installer", ClientID: reportClientID(), Consent: true,
		CreatedAt: time.Now().UnixMilli(), Description: desc, Versions: reportVersions{Installer: Version}}
	if id := os.Getenv("ANDRONIX_INSTALL_ID"); installIDRe.MatchString(id) {
		r.InstallID = id
	}
	r.Locale = reportLocale()
	logPath := ""
	if f != nil {
		in := &reportInstall{Command: f.Command, Status: "fail", Distro: f.Distro, Edition: f.Edition, Modded: f.Edition != "", Step: f.Step,
			Error: &reportError{Title: clip(f.Title, 200), Detail: clip(f.What, 2000)}}
		if t, err := time.Parse(time.RFC3339, f.Time); err == nil {
			in.At = t.UnixMilli()
		}
		r.Install, logPath = in, f.Log
		if f.Distro != "" {
			d, _ = conf.LoadDistro(f.Distro)
		}
	}
	de := ""
	if d != nil {
		de = Open(d).Get("DE")
		if r.Install != nil {
			r.Install.DE = de
		}
	}
	c := newCompat(d, de)
	p := c.facts.Probe
	r.Device = reportDevice{SDK: p.SDK, Kernel: p.Kernel, ROM: p.ROM, Arch: string(sys.DetectArch()), RAMMB: p.RAMMB, FreeStorageMB: p.FreeMB, Phantom: p.Phantom}
	if sys.IsTermux() {
		src := p.Termux
		switch src {
		case "f_droid":
			src = "fdroid"
		case "google_play_store":
			src = "play"
		case "github":
		default:
			src = "unknown"
		}
		r.Termux = &reportTermux{Installed: true, Source: src, Version: p.TermuxV}
	}
	if len(p.Syscalls) > 0 || len(c.plan.IDs) > 0 {
		r.Compat = &reportCompat{Probe: p.Syscalls, Rules: c.plan.IDs}
	}
	if inDistro() {
		guestFacts(r)
		if _, err := os.Stat(sessionLog()); err == nil && logPath == "" {
			logPath = sessionLog() // the desktop session's log (xstartup writes it)
		}
	}
	if logPath == "" {
		logPath = newestLog()
	}
	if logPath != "" {
		if b, err := os.ReadFile(logPath); err == nil {
			r.LogName = filepath.Base(logPath)
			r.Log = tail(string(b), reportLogLines, reportLogBytes)
		}
	}
	red := newRedactor()
	r.Description = red.do(r.Description)
	if r.Install != nil && r.Install.Error != nil {
		r.Install.Error.Title, r.Install.Error.Detail = red.do(r.Install.Error.Title), red.do(r.Install.Error.Detail)
	}
	r.Log = red.do(r.Log)
	r.Doctor = red.do(r.Doctor)
	return r
}

// guestFacts: inside a distro, what it is and the desktop session this
// terminal runs in (Android's own facts aren't reachable from here).
func guestFacts(r *problemReport) {
	rel := guestRelease()
	if r.Install == nil {
		r.Install = &reportInstall{} // which distro this is; no failed command
	}
	if r.Install.Distro == "" {
		r.Install.Distro = os.Getenv("ANDRONIX_DISTRO")
	}
	if r.Install.DE == "" {
		r.Install.DE = rel.Get("ANDRONIX_DE")
	}
	var b strings.Builder
	b.WriteString("inside the distro (andronix report in its terminal)\n")
	osr := conf.Parse(readFile("/etc/os-release"))
	fmt.Fprintf(&b, "os: %s\n", osr.Get("PRETTY_NAME"))
	for _, k := range []string{"ANDRONIX_DE", "ANDRONIX_EDITION", "ANDRONIX_VERSION"} {
		if v := rel.Get(k); v != "" {
			fmt.Fprintf(&b, "%s: %s\n", strings.ToLower(strings.TrimPrefix(k, "ANDRONIX_")), v)
		}
	}
	for _, k := range []string{"DISPLAY", "XDG_CURRENT_DESKTOP", "DESKTOP_SESSION", "PULSE_SERVER"} {
		if v := os.Getenv(k); v != "" {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
		}
	}
	fmt.Fprintf(&b, "uid: %d\n", os.Getuid())
	r.Doctor = b.String()
	// proot fakes uname's release (--kernel-release): the phone's is in /proc.
	if strings.HasSuffix(r.Device.Kernel, "-andronix") {
		r.Device.Kernel = ""
		if k := strings.TrimSpace(string(readFile("/proc/sys/kernel/osrelease"))); k != "" && !strings.HasSuffix(k, "-andronix") {
			r.Device.Kernel = k
		} else if f := strings.Fields(string(readFile("/proc/version"))); len(f) > 2 && f[1] == "version" {
			r.Device.Kernel = f[2]
		}
	}
}

// reportLocale is the user's language from LC_ALL, LC_MESSAGES or LANG
// ("pt_BR.UTF-8" is "pt-BR", "de" stays "de"), or "" when there's none or it's unclear: a
// malformed tag would make the server refuse the report.
func reportLocale() string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		v := os.Getenv(k)
		if v == "" {
			continue
		}
		// language[_REGION][.charset][@modifier]
		v, _, _ = strings.Cut(v, ".")
		v, _, _ = strings.Cut(v, "@")
		lang, region, _ := strings.Cut(strings.ReplaceAll(v, "-", "_"), "_")
		tag := strings.ToLower(lang)
		if region != "" {
			tag += "-" + strings.ToUpper(region)
		}
		if reLang.MatchString(tag) {
			return tag
		}
		if reLang.MatchString(strings.ToLower(lang)) {
			return strings.ToLower(lang) // an odd region: the language alone
		}
		return ""
	}
	return ""
}

var reLang = regexp.MustCompile(`^[a-z]{2,3}(-[A-Z]{2})?$`)

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// reportClientID is the report's idempotency key: a new UUID per report,
// kept in ~/.andronix/report-client-id until the server has it, so a
// retry after a network error doesn't file the report twice.
func reportClientID() string {
	p := filepath.Join(GetPaths().Home, "report-client-id")
	if b, err := os.ReadFile(p); err == nil {
		if id := strings.TrimSpace(string(b)); uuidRe.MatchString(id) {
			return id
		}
	}
	id := newUUID()
	os.MkdirAll(GetPaths().Home, 0o755)
	os.WriteFile(p, []byte(id+"\n"), 0o600)
	return id
}

var uuidRe = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// newestLog is the most recent install, update or remove log.
func newestLog() string {
	var best string
	var bestT time.Time
	for _, pat := range []string{"install-*.log", "update*.log", "remove-*.log"} {
		m, _ := filepath.Glob(filepath.Join(GetPaths().Logs, pat))
		for _, p := range m {
			if st, err := os.Stat(p); err == nil && st.ModTime().After(bestT) {
				best, bestT = p, st.ModTime()
			}
		}
	}
	return best
}

// tail is the last n lines of s, at most max bytes.
func tail(s string, n, max int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	out := strings.Join(lines, "\n")
	if len(out) > max {
		out = out[len(out)-max:]
		if i := strings.IndexByte(out, '\n'); i >= 0 {
			out = out[i+1:]
		}
	}
	return out
}

// redactor removes what could identify the user or unlock a purchase.
type redactor struct {
	home  string
	users []string
}

func newRedactor() *redactor {
	r := &redactor{home: sys.Home()}
	// The first-boot user of every installed distro.
	m, _ := filepath.Glob(filepath.Join(GetPaths().Distros, "*/rootfs/etc/andronix/user"))
	seen := map[string]bool{}
	for _, p := range m {
		if b, err := os.ReadFile(p); err == nil {
			if u := strings.TrimSpace(string(b)); len(u) >= 2 && u != "root" && !seen[u] {
				seen[u] = true
				r.users = append(r.users, u)
			}
		}
	}
	sort.Slice(r.users, func(i, j int) bool { return len(r.users[i]) > len(r.users[j]) })
	return r
}

var (
	reTokenFlag = regexp.MustCompile(`(--token[= ]+)('[^']*'|"[^"]*"|\S+)`)
	reTokenKV   = regexp.MustCompile(`\b[kehfpv]=[^&\s'"]+(?:&[kehfpv]=[^&\s'"]+)+`)
	reQuery     = regexp.MustCompile(`(https?://[^\s?'"]+)\?[^\s'"]*`)
	reEmail     = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`)
	reAppUID    = regexp.MustCompile(`\bu\d+_a\d+\b`)
	reAuth      = regexp.MustCompile(`(?i)(authorization|password|passwd)(["']?\s*[:=]\s*["']?)(?:bearer\s+)?[^\s"',]+`)
	reBearer    = regexp.MustCompile(`(?i)\bbearer\s+[^\s"',]+`)
	reTermuxHom = regexp.MustCompile(`/data/(?:data|user/\d+)/com\.termux/files/home`)
)

func (r *redactor) do(s string) string {
	if s == "" {
		return s
	}
	s = reTokenFlag.ReplaceAllString(s, "${1}<token>")
	s = reQuery.ReplaceAllString(s, "${1}?<redacted>")
	s = reTokenKV.ReplaceAllString(s, "<token>")
	s = reEmail.ReplaceAllString(s, "<email>")
	s = reAuth.ReplaceAllString(s, "${1}${2}<redacted>")
	s = reBearer.ReplaceAllString(s, "Bearer <redacted>")
	s = reTermuxHom.ReplaceAllString(s, "~")
	if r.home != "" && r.home != "/" {
		s = strings.ReplaceAll(s, r.home, "~")
	}
	s = reAppUID.ReplaceAllString(s, "<uid>")
	for _, u := range r.users {
		s = regexp.MustCompile(`\b`+regexp.QuoteMeta(u)+`\b`).ReplaceAllString(s, "<user>")
	}
	return s
}

// Report is `andronix report [--dry-run] [--message TEXT] [--open AX-ID]`.
// It uploads the report as a draft (reports-v1 draft 3) and opens the
// Andronix app on it, where the user adds details or photos and sends it.
// Without the app (or inside a distro, where Android's am isn't reachable)
// it prints the link and can send the report as it is.
func Report(ctx context.Context, dryRun bool, message, openID string) error {
	if openID != "" {
		return openDraft(ctx, strings.ToUpper(strings.TrimSpace(openID)))
	}
	desc := clip(strings.TrimSpace(message), reportDescMax)
	rep := buildReport(desc)
	body, _ := json.MarshalIndent(rep, "", "  ")
	if dryRun {
		fmt.Println(string(body))
		return nil
	}
	fmt.Println()
	ui.Section("This is everything that will be sent to Andronix")
	ui.Note("Tokens, download links, email addresses, user names and your home folder were removed.")
	fmt.Println()
	fmt.Println(string(body))
	fmt.Println()
	ok, err := ui.Confirm("Send it to Andronix?", "It goes to the Andronix app next, where you can add details and photos.", false)
	if err != nil {
		return err
	}
	if !ok {
		ui.Note("Nothing was sent.")
		return nil
	}
	d, err := postDraft(ctx, rep)
	if err != nil {
		return err
	}
	saveDraft(d)
	fmt.Println()
	if openLink(d.Link) {
		ui.OK("Report " + d.ID + " is in the Andronix app: add what happened (and photos if they help), then send it there.")
		ui.Note("It waits there for 24 hours.")
		fmt.Println()
		return nil
	}
	ui.Info("Report " + d.ID + " is ready. To add details or photos, open it in the Andronix app:")
	fmt.Println("  " + d.Link)
	if inDistro() {
		ui.Note("Or open it from Termux: andronix report --open " + d.ID)
	}
	ok, err = ui.Confirm("Send it now without the app?", "", true)
	if err != nil || !ok {
		ui.Note("Not sent yet. The link works for 24 hours.")
		return err
	}
	if desc == "" {
		desc, err = ui.Input("What happened, in one line?", "For example: the desktop stays black after install.", "", func(s string) error {
			if strings.TrimSpace(s) == "" {
				return fmt.Errorf("a few words, please")
			}
			return nil
		})
		if err != nil {
			return ui.Errorf("No description", "Sending without the app needs a line about what happened.", `Add it: andronix report --message "what happened"`)
		}
		desc = clip(newRedactor().do(strings.TrimSpace(desc)), reportDescMax)
	}
	if err := finalizeDraft(ctx, d, desc); err != nil {
		return err
	}
	removeDraft(d.ID)
	fmt.Println()
	ui.OK("Report sent: " + d.ID)
	ui.Note("Quote " + d.ID + " on Discord (https://chat.andronix.app) or by email (support@andronix.app).")
	fmt.Println()
	return nil
}

// reportDraft is the server's answer to a draft upload. The claim token
// is a secret: it's kept only in a 0600 file until the report is sent,
// and never logged.
type reportDraft struct {
	ID         string `json:"id"`
	ClaimToken string `json:"claim_token"`
	ExpiresAt  string `json:"expires_at"`
	Link       string `json:"link"`
}

// inDistro: running inside a distro (the in-distro copy of andronix).
func inDistro() bool { return os.Getenv("ANDRONIX_DISTRO") != "" }

// draftDir is where a draft waits: inside a distro its /tmp (Termux reads
// it through the rootfs for --open), else ~/.andronix/report-drafts.
func draftDir() string {
	if inDistro() {
		return "/tmp/andronix-report-drafts"
	}
	return filepath.Join(GetPaths().Home, "report-drafts")
}

func saveDraft(d *reportDraft) {
	os.MkdirAll(draftDir(), 0o700)
	b, _ := json.Marshal(d)
	os.WriteFile(filepath.Join(draftDir(), d.ID+".json"), b, 0o600)
}

func removeDraft(id string) { os.Remove(filepath.Join(draftDir(), id+".json")) }

// findDraft looks in Termux's drafts and every distro's /tmp.
func findDraft(id string) *reportDraft {
	paths := []string{filepath.Join(draftDir(), id+".json")}
	m, _ := filepath.Glob(filepath.Join(GetPaths().Distros, "*/rootfs/tmp/andronix-report-drafts", id+".json"))
	for _, p := range append(paths, m...) {
		var d reportDraft
		if b, err := os.ReadFile(p); err == nil && json.Unmarshal(b, &d) == nil && d.ID == id && d.Link != "" {
			return &d
		}
	}
	return nil
}

// openDraft is `andronix report --open AX-ID` (in Termux).
func openDraft(ctx context.Context, id string) error {
	if !reportIDRe.MatchString(id) {
		return ui.Errorf("Not a report id", "'"+id+"' doesn't look like AX-XXXXXX.", "Copy the id andronix report printed.")
	}
	d := findDraft(id)
	if d == nil {
		return ui.Errorf("Report "+id+" isn't here", "There's no waiting report with that id on this phone.", "Run andronix report again.")
	}
	if t, err := time.Parse(time.RFC3339, d.ExpiresAt); err == nil && time.Now().After(t) {
		return ui.Errorf("Report "+id+" expired", "Reports wait 24 hours for the app.", "Run andronix report again.")
	}
	if !openLink(d.Link) {
		return ui.Errorf("Couldn't open the Andronix app", "Android didn't open the report link.", "Install or update the Andronix app, then run andronix report --open "+id+" again.")
	}
	ui.OK("Report " + id + " is in the Andronix app: add what happened, then send it there.")
	return nil
}

// openLink opens an andronix:// link in the app with Termux's am. It
// can't inside a distro (no Android there).
func openLink(link string) bool {
	if inDistro() || !sys.IsTermux() || !strings.HasPrefix(link, "andronix://report/") {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := sys.CommandContext(ctx, "am", "start", "-a", "android.intent.action.VIEW", "-d", link).CombinedOutput()
	// am exits 0 even when nothing handles the link; it says "Error" then.
	return err == nil && !strings.Contains(string(out), "Error")
}

// reportIDRe: AX- and 6 Crockford base32 characters (the contract).
var reportIDRe = regexp.MustCompile(`^AX-[0-9A-HJKMNP-TV-Z]{6}$`)

// postDraft uploads the report as a draft. 5xx and network errors retry
// with the same client_id (the server answers a replay with the original
// draft); 413 trims the log once.
func postDraft(ctx context.Context, rep *problemReport) (*reportDraft, error) {
	var lastErr error
	for try := 0; try < 3; try++ {
		if try > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(time.Duration(try*try*2) * time.Second):
			}
		}
		body, _ := json.Marshal(rep)
		var d reportDraft
		status, err := postJSON(ctx, "/v1/reports?draft=1", body, nil, &d)
		if err == nil && (!reportIDRe.MatchString(d.ID) || d.ClaimToken == "" || !strings.HasPrefix(d.Link, "andronix://report/draft/"+d.ID)) {
			err = ui.Errorf("Couldn't send the report", "The report service's answer wasn't understood.", "Try again later, or email support@andronix.app.")
		}
		switch {
		case err == nil:
			os.Remove(filepath.Join(GetPaths().Home, "report-client-id"))
			return &d, nil
		case status == http.StatusRequestEntityTooLarge && len(rep.Log) > 4096:
			rep.Log = tail(rep.Log, 100, len(rep.Log)/4)
			lastErr = err
		case status == 0 || status >= 500:
			lastErr = err
		default:
			return nil, err
		}
	}
	return nil, lastErr
}

// finalizeDraft sends the draft as it is (no app), with the user's line.
func finalizeDraft(ctx context.Context, d *reportDraft, desc string) error {
	body, _ := json.Marshal(map[string]any{"client_id": newUUID(), "consent": true, "description": desc})
	var out struct {
		ID string `json:"id"`
	}
	for try := 0; try < 3; try++ {
		if try > 0 {
			time.Sleep(time.Duration(try*try*2) * time.Second)
		}
		status, err := postJSON(ctx, "/v1/reports/drafts/"+d.ID+"/finalize", body, map[string]string{"X-Report-Claim": d.ClaimToken}, &out)
		switch {
		case err == nil:
			return nil
		case status == http.StatusGone:
			removeDraft(d.ID)
			return ui.Errorf("Report "+d.ID+" expired", "Reports wait 24 hours.", "Run andronix report again.")
		case status == http.StatusUnauthorized:
			return ui.Errorf("Report "+d.ID+" was already sent", "The Andronix app (or another try) sent it.", "Nothing more to do. Quote "+d.ID+" if you ask for help.")
		case status != 0 && status < 500:
			return err
		}
		if try == 2 {
			return err
		}
	}
	return nil
}

func newUUID() string {
	if b, err := os.ReadFile("/proc/sys/kernel/random/uuid"); err == nil {
		if id := strings.TrimSpace(string(b)); uuidRe.MatchString(id) {
			return id
		}
	}
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// postJSON posts to products-api and decodes a 2xx answer into out. The
// status is 0 when nothing came back.
func postJSON(ctx context.Context, path string, body []byte, headers map[string]string, out any) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var resp *http.Response
	var err error
	for _, api := range netx.APIs() { // the next address only if this one can't be reached
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, api+path, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "andronix/"+Version)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		if resp, err = http.DefaultClient.Do(req); !netx.Unreachable(ctx, err) {
			break
		}
	}
	if err != nil {
		return 0, ui.Errorf("Couldn't send the report", "No connection to the report service.", "Check your internet and run andronix report again; it won't be filed twice.")
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
	st := resp.StatusCode
	if st/100 == 2 {
		if json.Unmarshal(b, out) != nil {
			return st, ui.Errorf("Couldn't send the report", "The report service's answer wasn't understood.", "Try again later, or email support@andronix.app.")
		}
		return st, nil
	}
	var e struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Field   string `json:"field"`
	}
	json.Unmarshal(b, &e)
	switch {
	case st == http.StatusTooManyRequests:
		wait := "a while"
		if n, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && n > 0 {
			wait = fmt.Sprintf("%d minutes", (n+59)/60)
		}
		return st, ui.Errorf("Too many reports", "The report service limits how many reports a phone sends.", "Try again in "+wait+".")
	case st == http.StatusNotFound && e.Error == "disabled":
		return st, ui.Errorf("Reports aren't open yet", "The report service isn't switched on.", "Ask on Discord (https://chat.andronix.app) or email support@andronix.app.")
	case st/100 == 4:
		what := fmt.Sprintf("The report service refused it (%d %s).", st, e.Error)
		if e.Message != "" {
			what = "The report service refused it: " + e.Message
			if e.Field != "" {
				what += " (" + e.Field + ")"
			}
		}
		return st, ui.Errorf("Couldn't send the report", what, "Update andronix (andronix update --self) and try again.")
	}
	return st, ui.Errorf("Couldn't send the report", fmt.Sprintf("The report service answered %d.", st), "Try again later; it won't be filed twice.")
}
