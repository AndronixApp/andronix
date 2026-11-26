package pkgmgr

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// AptSuite is one suite the apt sources use, with its first component.
type AptSuite struct{ Name, Component string }

// AptSuites lists the suites the apt sources under root take from base
// (deb lines and deb822 stanzas; deb-src and disabled ones are left out).
// It's what a mirror must carry to stand in for base.
func AptSuites(root, base string) []AptSuite {
	base = strings.TrimSuffix(base, "/")
	var out []AptSuite
	seen := map[string]bool{}
	add := func(uri, suite, comp string) {
		if strings.TrimSuffix(uri, "/") != base || suite == "" || strings.HasSuffix(suite, "/") || seen[suite] {
			return
		}
		seen[suite] = true
		out = append(out, AptSuite{suite, comp})
	}
	lists, _ := filepath.Glob(filepath.Join(root, "etc/apt/sources.list.d/*.list"))
	for _, f := range append([]string{filepath.Join(root, "etc/apt/sources.list")}, lists...) {
		b, _ := os.ReadFile(f)
		for _, l := range strings.Split(string(b), "\n") {
			l = strings.TrimSpace(l)
			if !strings.HasPrefix(l, "deb ") && !strings.HasPrefix(l, "deb\t") {
				continue
			}
			l = l[3:]
			if i, j := strings.Index(l, "["), strings.Index(l, "]"); i >= 0 && j > i {
				l = l[:i] + " " + l[j+1:] // [arch=... signed-by=...]
			}
			f := strings.Fields(l)
			if len(f) < 2 {
				continue
			}
			comp := ""
			if len(f) > 2 {
				comp = f[2]
			}
			add(f[0], f[1], comp)
		}
	}
	srcs, _ := filepath.Glob(filepath.Join(root, "etc/apt/sources.list.d/*.sources"))
	for _, f := range srcs {
		b, _ := os.ReadFile(f)
		for _, st := range deb822(string(b)) {
			if !hasWord(st["types"], "deb") {
				continue
			}
			switch strings.ToLower(st["enabled"]) {
			case "no", "false", "0", "off":
				continue
			}
			comp := ""
			if c := strings.Fields(st["components"]); len(c) > 0 {
				comp = c[0]
			}
			for _, u := range strings.Fields(st["uris"]) {
				for _, s := range strings.Fields(st["suites"]) {
					add(u, s, comp)
				}
			}
		}
	}
	return out
}

func hasWord(s, w string) bool {
	for _, f := range strings.Fields(s) {
		if f == w {
			return true
		}
	}
	return false
}

// deb822 splits a .sources file into stanzas of lower-cased field names;
// a value that goes on over several lines is joined with spaces.
func deb822(s string) []map[string]string {
	var out []map[string]string
	cur := map[string]string{}
	key := ""
	flush := func() {
		if len(cur) > 0 {
			out = append(out, cur)
		}
		cur, key = map[string]string{}, ""
	}
	for _, l := range strings.Split(s, "\n") {
		l = strings.TrimRight(l, "\r")
		switch {
		case strings.TrimSpace(l) == "":
			flush()
		case strings.HasPrefix(l, "#"):
		case l[0] == ' ' || l[0] == '\t':
			if key != "" {
				cur[key] += " " + strings.TrimSpace(l)
			}
		default:
			k, v, ok := strings.Cut(l, ":")
			if !ok {
				continue
			}
			key = strings.ToLower(strings.TrimSpace(k))
			cur[key] = strings.TrimSpace(v)
		}
	}
	flush()
	return out
}

// ReleaseDate is the Date field of a Release or InRelease file (its start
// is enough), when the archive's publisher made it.
func ReleaseDate(b []byte) (time.Time, bool) {
	for _, l := range strings.Split(string(b), "\n") {
		v, ok := strings.CutPrefix(l, "Date:")
		if !ok {
			continue
		}
		// "Sun, 08 Nov 2026  7:49:44 UTC": the hour may be space-padded.
		v = strings.Join(strings.Fields(v), " ")
		for _, layout := range []string{"Mon, 2 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 -0700", "2 Jan 2006 15:04:05 MST"} {
			if t, err := time.Parse(layout, v); err == nil {
				return t.UTC(), true
			}
		}
		return time.Time{}, false
	}
	return time.Time{}, false
}

// AptListDate is the Date of the Release file apt has on disk for base's
// suite (from the last refresh that worked), or the zero time.
func AptListDate(root, base, suite string) time.Time {
	name := aptListName(base, suite)
	for _, f := range []string{"_InRelease", "_Release"} {
		if b, err := readHead(filepath.Join(root, "var/lib/apt/lists", name+f), 8<<10); err == nil {
			if t, ok := ReleaseDate(b); ok {
				return t
			}
		}
	}
	return time.Time{}
}

// aptListName is the start of the names apt gives the list files of base's
// suite (".../dists/<suite>"): these characters %-quoted, then "/" as "_".
func aptListName(base, suite string) string {
	name := strings.TrimSuffix(base, "/")
	if i := strings.Index(name, "://"); i >= 0 {
		name = name[i+3:]
	}
	var q strings.Builder
	for _, c := range []byte(name + "/dists/" + suite) {
		switch {
		case c == '/':
			q.WriteByte('_')
		case c <= 0x20 || c >= 0x7f || strings.IndexByte(`\|{}[]<>"^~_=!@#$%&*`, c) >= 0:
			fmt.Fprintf(&q, "%%%02x", c)
		default:
			q.WriteByte(c)
		}
	}
	return q.String()
}

// AptHasIndex reports whether apt has a package index on disk for base's
// suite (a "..._dists_<suite>_<component>_binary-<arch>_Packages" file,
// compressed or not): what it needs to know a single package of it. The
// Release file alone (AptListDate) doesn't give it one.
func AptHasIndex(root, base, suite string) bool {
	prefix := aptListName(base, suite) + "_"
	ents, _ := os.ReadDir(filepath.Join(root, "var/lib/apt/lists"))
	for _, e := range ents {
		if n := e.Name(); !e.IsDir() && strings.HasPrefix(n, prefix) && strings.Contains(n[len(prefix):], "_Packages") {
			return true
		}
	}
	return false
}

// readHead reads up to n bytes from the start of a file.
func readHead(path string, n int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b := make([]byte, n)
	m, err := f.Read(b)
	if m == 0 && err != nil {
		return nil, err
	}
	return b[:m], nil
}
