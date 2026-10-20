package ui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// --progress json: one JSON object per line on stdout, for the Andronix
// app's own install screen (a versioned contract, docs/progress-json.md).
// Everything meant for people (banners, boxes, apt output) goes to stderr
// instead. Events, by "t":
//
//	hello     {"v":1,"installer":"2.1.0","host":"standalone"}
//	step      {"n":3,"of":12,"key":"desktop","label":"Installing XFCE desktop"}
//	progress  {"n":3,"cur":123,"total":456,"unit":"bytes"|"packages"|""}
//	ok|skip|fail {"n":3,"key":...,"label":...,"detail":...,"count":252,"secs":91,"reason":...}
//	note|warn {"text":...}
//	error     {"title","what","fix","class","kind","detail","log"}
//	done      {"status":"ok"|"fail"|"cancelled","outcome","distro","edition"}
var JSON bool

var (
	jsonOut  *os.File
	jsonMu   sync.Mutex
	doneInfo = map[string]any{}

	// The run's log gets every event too ("progress: {json}"), for problem
	// reports. Events before the log opens wait in pending; `progress`
	// events go in at most every 10 s, the rest all.
	eventLog     func(string)
	pending      []string
	lastProgress time.Time
)

// SetEventLog sends every later event, and those waiting, to f (the run's
// log), one JSON object per call.
func SetEventLog(f func(string)) {
	jsonMu.Lock()
	eventLog = f
	p := pending
	pending = nil
	jsonMu.Unlock()
	for _, l := range p {
		if f != nil {
			f(l)
		}
	}
}

// EnableJSON switches to --progress json. Call it before anything prints:
// it keeps the real stdout for events and points fd 1 (and os.Stdout) at
// stderr, so child processes can't mix their output into the stream.
func EnableJSON() error {
	fd, err := syscall.Dup(1)
	if err != nil {
		return err
	}
	jsonOut = os.NewFile(uintptr(fd), "progress-json")
	if err := dupTo(2, 1); err != nil {
		return err
	}
	os.Stdout = os.Stderr
	JSON, Plain, Yes = true, true, true
	Interactive = false
	return nil
}

// Emit writes one event (no-op unless JSON).
func Emit(t string, fields map[string]any) {
	if !JSON {
		return
	}
	m := map[string]any{"t": t}
	for k, v := range fields {
		if v != "" && v != nil {
			m[k] = v
		}
	}
	b, err := json.Marshal(m)
	if err != nil {
		return
	}
	jsonMu.Lock()
	if jsonOut != nil {
		jsonOut.Write(append(b, '\n'))
	}
	toLog := t != "progress" || time.Since(lastProgress) >= 10*time.Second
	if t == "progress" && toLog {
		lastProgress = time.Now()
	}
	f := eventLog
	if toLog && f == nil && len(pending) < 200 {
		pending = append(pending, string(b))
	}
	jsonMu.Unlock()
	if toLog && f != nil {
		f(string(b))
	}
}

// SetDone adds a field to the final done event (distro, edition, outcome).
func SetDone(k string, v any) { doneInfo[k] = v }

// EmitDone writes the done event for err.
func EmitDone(err error) {
	f := map[string]any{"status": "ok"}
	switch {
	case err != nil && errors.Is(err, ErrCancelled):
		f["status"] = "cancelled"
	case err != nil && errors.Is(err, context.Canceled):
		f["status"] = "cancelled"
	case err != nil:
		f["status"] = "fail"
	}
	for k, v := range doneInfo {
		f[k] = v
	}
	Emit("done", f)
}

// EmitError writes the error event for err.
func EmitError(err error) {
	ue, ok := err.(*UserError)
	if !ok {
		var u *UserError
		if errors.As(err, &u) {
			ue, ok = u, true
		}
	}
	if !ok {
		ue = &UserError{Title: "Something went wrong", What: err.Error()}
	}
	Emit("error", map[string]any{"title": ue.Title, "what": ue.What, "fix": ue.Fix, "class": ClassOf(ue),
		"kind": ue.Kind, "detail": ue.Detail, "log": ue.Log})
}

var classSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ClassOf is a failure's class: its Class, else a slug of its title (the
// same rule as telemetry's error_class).
func ClassOf(ue *UserError) string {
	if ue.Class != "" {
		return ue.Class
	}
	s := strings.Trim(classSlug.ReplaceAllString(strings.ToLower(ue.Title), "_"), "_")
	if len(s) > 48 {
		s = s[:48]
	}
	return s
}

// The steps in JSON mode.
type jsonRep struct {
	n             int
	label, detail string
	detailKey     string         // the detail's stable key for the app (DetailOf), "" if none
	detailArgs    map[string]any // its values: integers, strings
	last          time.Time
}

func (j *jsonRep) Progress(cur, total int64, unit string) {
	if time.Since(j.last) < 250*time.Millisecond && cur < total {
		return
	}
	j.last = time.Now()
	Emit("progress", map[string]any{"n": j.n, "cur": cur, "total": total, "unit": unit})
}
func (j *jsonRep) Line(string)     {}
func (j *jsonRep) Detail(s string) { j.detail, j.detailKey, j.detailArgs = s, "", nil }
func (j *jsonRep) detailOf(key string, args map[string]any, s string) {
	j.Detail(s)
	j.detailKey, j.detailArgs = key, args
}

// keyedDetail is a Reporter that also passes a detail's key and values
// (only the JSON one: the app translates by key).
type keyedDetail interface {
	detailOf(key string, args map[string]any, s string)
}

// DetailOf is Detail with a stable key and its values for the app (card
// #174): the ok event gets detail_key and detail_args next to the English
// detail, which stays exactly as before. Keys and arg names never change
// once shipped (a change is a new key); see docs/progress-json.md.
func DetailOf(r Reporter, key, s string, args map[string]any) {
	if k, ok := r.(keyedDetail); ok {
		k.detailOf(key, args, s)
		return
	}
	r.Detail(s)
}
func (j *jsonRep) Label(s string) { j.label = s }

var leadingCount = regexp.MustCompile(`^(\d+) `)

// StepKey is a step's key: Key, else a slug of its label.
func StepKey(key, label string) string {
	if key == "" {
		key = strings.Trim(classSlug.ReplaceAllString(strings.ToLower(label), "-"), "-")
	}
	return key
}

func runJSON(ctx context.Context, steps []Step, log Logger) error {
	for i, s := range steps {
		key := StepKey(s.Key, s.Label)
		rep := &jsonRep{n: i + 1, label: s.Label}
		Emit("step", map[string]any{"n": i + 1, "of": len(steps), "key": key, "label": s.Label})
		log.Printf("== step: %s", s.Label)
		start := time.Now()
		err := s.Run(ctx, rep)
		f := map[string]any{"n": i + 1, "key": key, "label": rep.label, "secs": int(time.Since(start).Seconds())}
		var sk skipErr
		switch {
		case errors.As(err, &sk):
			f["reason"] = sk.reason
			Emit("skip", f)
		case err != nil:
			log.Printf("step failed: %v", err)
			Emit("fail", f)
			return err
		default:
			f["detail"] = rep.detail
			if rep.detailKey != "" {
				f["detail_key"] = rep.detailKey
				args := rep.detailArgs
				if args == nil {
					args = map[string]any{}
				}
				f["detail_args"] = args
			}
			if m := leadingCount.FindStringSubmatch(rep.detail); m != nil {
				n, _ := strconv.Atoi(m[1])
				f["count"] = n
			}
			Emit("ok", f)
		}
	}
	return nil
}
