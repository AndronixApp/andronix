package sys

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// legacyZones are old names Android still reports that some distros ship
// only in tzdata-legacy (Debian and Ubuntu split the backward links out),
// for a rootfs whose tzdata.zi doesn't list them.
var legacyZones = map[string]string{
	"Asia/Calcutta":        "Asia/Kolkata",
	"Asia/Katmandu":        "Asia/Kathmandu",
	"Asia/Saigon":          "Asia/Ho_Chi_Minh",
	"Asia/Rangoon":         "Asia/Yangon",
	"Asia/Dacca":           "Asia/Dhaka",
	"Asia/Thimbu":          "Asia/Thimphu",
	"Asia/Ulan_Bator":      "Asia/Ulaanbaatar",
	"Asia/Macao":           "Asia/Macau",
	"Asia/Ujung_Pandang":   "Asia/Makassar",
	"Europe/Kiev":          "Europe/Kyiv",
	"Atlantic/Faeroe":      "Atlantic/Faroe",
	"America/Godthab":      "America/Nuuk",
	"America/Buenos_Aires": "America/Argentina/Buenos_Aires",
	"America/Cordoba":      "America/Argentina/Cordoba",
	"America/Indianapolis": "America/Indiana/Indianapolis",
	"America/Louisville":   "America/Kentucky/Louisville",
	"Africa/Asmera":        "Africa/Nairobi",
	"Africa/Lusaka":        "Africa/Maputo",
	"Pacific/Truk":         "Pacific/Port_Moresby",
	"Pacific/Ponape":       "Pacific/Guadalcanal",
	"Pacific/Enderbury":    "Pacific/Kanton",
}

// ResolveZone returns the name under root's /usr/share/zoneinfo for the
// time zone tz: tz itself when its file is there, else the zone an old
// name links to (the rootfs's tzdata.zi, then legacyZones), else the same
// name in another case ("europe/berlin"). "" when none is there.
func ResolveZone(root, tz string) string {
	tz = strings.TrimSpace(tz)
	if tz == "" || strings.Contains(tz, "..") || strings.HasPrefix(tz, "/") {
		return ""
	}
	dir := filepath.Join(root, "usr/share/zoneinfo")
	has := func(z string) bool {
		st, err := os.Stat(filepath.Join(dir, z))
		// The exact case too: a case-insensitive disk (macOS, tests)
		// would accept "europe/berlin" for /etc/localtime.
		return err == nil && st.Mode().IsRegular() && zoneFold(dir, z) == z
	}
	if has(tz) {
		return tz
	}
	links := zoneLinks(dir)
	for z, n := tz, 0; n < 4; n++ {
		t, ok := links[z]
		if !ok {
			t, ok = legacyZones[z]
		}
		if !ok {
			break
		}
		if has(t) {
			return t
		}
		z = t
	}
	if z := zoneFold(dir, tz); z != "" && z != tz {
		return ResolveZone(root, z)
	}
	return ""
}

// zoneLinks reads the "L target name" lines of tzdata.zi (name -> target).
func zoneLinks(dir string) map[string]string {
	m := map[string]string{}
	f, err := os.Open(filepath.Join(dir, "tzdata.zi"))
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if p := strings.Fields(sc.Text()); len(p) == 3 && p[0] == "L" {
			m[p[2]] = p[1]
		}
	}
	return m
}

// zoneFold finds tz's path under dir ignoring case, one part at a time.
func zoneFold(dir, tz string) string {
	var out []string
	for _, part := range strings.Split(tz, "/") {
		ents, err := os.ReadDir(filepath.Join(append([]string{dir}, out...)...))
		if err != nil {
			return ""
		}
		found := ""
		for _, e := range ents {
			if strings.EqualFold(e.Name(), part) {
				found = e.Name()
				break
			}
		}
		if found == "" {
			return ""
		}
		out = append(out, found)
	}
	return strings.Join(out, "/")
}
