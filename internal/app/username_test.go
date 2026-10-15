package app

import (
	"strings"
	"testing"
)

// The username prompt says why a name was refused (support #220).
func TestUserNameProblem(t *testing.T) {
	for in, want := range map[string]string{
		"alex":                  "",
		"alex_2":                "",
		"a-b":                   "",
		"":                      "type a name",
		"Max":                   "use lower-case letters: max",
		"max Geheim!1":          "no spaces",
		"max:Geheim!":           "':' isn't allowed",
		"jürgen":                "'ü' isn't allowed",
		"2alex":                 "start with a lower-case letter",
		"_alex":                 "start with a lower-case letter",
		"root":                  "other than root",
		strings.Repeat("a", 33): "at most 32",
	} {
		got := userNameProblem(in)
		if want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}
