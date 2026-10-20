package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

func TestProgressJSON(t *testing.T) {
	r, w, _ := os.Pipe()
	JSON, jsonOut = true, w
	defer func() { JSON, jsonOut = false, nil }()
	steps := []Step{
		{Key: "update", Label: "Updating Debian 13", Run: func(ctx context.Context, rep Reporter) error {
			rep.Progress(1, 1, "packages")
			rep.Detail("1 package")
			return nil
		}},
		{Label: "Installing a web browser", Run: func(ctx context.Context, rep Reporter) error { return Skip("not requested") }},
		{Key: "desktop", Label: "Installing XFCE desktop", Run: func(ctx context.Context, rep Reporter) error {
			return &UserError{Title: "Couldn't install xfce desktop", What: "apt stopped", Class: "package_manager", Kind: "network_dns"}
		}},
	}
	err := runJSON(context.Background(), steps, nopLog{})
	EmitError(err)
	SetDone("distro", "debian")
	EmitDone(err)
	w.Close()
	var ev []map[string]any
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var m map[string]any
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			t.Fatalf("not JSON: %s", sc.Text())
		}
		ev = append(ev, m)
	}
	want := []string{"step", "progress", "ok", "step", "skip", "step", "fail", "error", "done"}
	if len(ev) != len(want) {
		t.Fatalf("got %d events: %v", len(ev), ev)
	}
	for i, w := range want {
		if ev[i]["t"] != w {
			t.Errorf("event %d: %v, want %s", i, ev[i]["t"], w)
		}
	}
	if ev[2]["key"] != "update" || ev[2]["count"] != float64(1) || ev[2]["detail"] != "1 package" {
		t.Errorf("ok: %v", ev[2])
	}
	if ev[3]["key"] != "installing-a-web-browser" || ev[4]["reason"] != "not requested" {
		t.Errorf("skip: %v %v", ev[3], ev[4])
	}
	if ev[7]["class"] != "package_manager" || ev[7]["kind"] != "network_dns" {
		t.Errorf("error: %v", ev[7])
	}
	if ev[8]["status"] != "fail" || ev[8]["distro"] != "debian" {
		t.Errorf("done: %v", ev[8])
	}
	_ = errors.New
}

// Keyed details (card #174): the ok event keeps the English detail and adds
// detail_key and detail_args (integers stay integers); a later plain Detail
// drops the key; the other reporters just show the text.
func TestDetailOf(t *testing.T) {
	r, w, _ := os.Pipe()
	JSON, jsonOut = true, w
	defer func() { JSON, jsonOut = false, nil }()
	steps := []Step{
		{Key: "download", Label: "Finding the best download", Run: func(ctx context.Context, rep Reporter) error {
			DetailOf(rep, "source.image", "ready-made image, 205.9 MB", map[string]any{"bytes": int64(215901798)})
			return nil
		}},
		{Key: "verify", Label: "Checking", Run: func(ctx context.Context, rep Reporter) error {
			DetailOf(rep, "verify.sha256", "sha256 checked", nil)
			return nil
		}},
		{Key: "x", Label: "Later plain detail", Run: func(ctx context.Context, rep Reporter) error {
			DetailOf(rep, "packages", "3 packages", map[string]any{"count": 3})
			rep.Detail("something else")
			return nil
		}},
	}
	if err := runJSON(context.Background(), steps, nopLog{}); err != nil {
		t.Fatal(err)
	}
	w.Close()
	var ok []map[string]any
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		var m map[string]any
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.UseNumber()
		if dec.Decode(&m) != nil {
			t.Fatalf("not JSON: %s", sc.Text())
		}
		if m["t"] == "ok" {
			ok = append(ok, m)
		}
	}
	if len(ok) != 3 {
		t.Fatalf("ok events: %v", ok)
	}
	a, _ := ok[0]["detail_args"].(map[string]any)
	if ok[0]["detail"] != "ready-made image, 205.9 MB" || ok[0]["detail_key"] != "source.image" || a["bytes"] != json.Number("215901798") {
		t.Errorf("image: %v", ok[0])
	}
	if a, ok2 := ok[1]["detail_args"].(map[string]any); ok[1]["detail_key"] != "verify.sha256" || !ok2 || len(a) != 0 {
		t.Errorf("no args: want detail_args {} (%v)", ok[1])
	}
	if _, has := ok[2]["detail_key"]; has || ok[2]["detail"] != "something else" {
		t.Errorf("plain Detail after DetailOf kept the key: %v", ok[2])
	}

	p := &plainRep{}
	DetailOf(p, "packages", "3 packages", map[string]any{"count": 3})
	if p.detail != "3 packages" {
		t.Errorf("plain reporter: %q", p.detail)
	}
}
