package app

import (
	"encoding/json"
	"io"
	"os"
	"testing"
)

// selfcheck --json carries the probe summary and the matching rules (for
// problem reports), whatever the checks say.
func TestSelfCheckJSONHasProbeAndRules(t *testing.T) {
	t.Setenv("ANDRONIX_HOME", t.TempDir())
	r, w, _ := os.Pipe()
	old := os.Stdout
	os.Stdout = w
	SelfCheck(true) // fails off a phone; only the shape matters here
	os.Stdout = old
	w.Close()
	b, _ := io.ReadAll(r)
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
	p, ok := m["probe"].(map[string]any)
	if !ok || p["summary"] == "" || p["kernel"] == nil {
		t.Errorf("probe: %v", m["probe"])
	}
	if _, ok := m["rules"].([]any); !ok {
		t.Errorf("rules isn't a list: %v", m["rules"])
	}
}
