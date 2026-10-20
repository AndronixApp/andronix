package pkgmgr

import (
	"regexp"
	"strconv"
)

// apt's download progress, from its plain output (no TTY): "Need to get
// 26.8 MB of archives." (or "26.8 MB/30.1 MB" when part is cached) is the
// total, and every "Get:N <url> <dist> <pkg> <ver> [4352 kB]" a done file.
// apt counts in SI units (kB = 1000 B).
var (
	reAptNeed = regexp.MustCompile(`^Need to get ([0-9.,]+) (B|kB|MB|GB)\b`)
	reAptGet  = regexp.MustCompile(`^Get:\d+ .*\[([0-9.,]+) (B|kB|MB|GB)\]\s*$`)
)

// AptNeedBytes is the total download size from a "Need to get" line.
func AptNeedBytes(line string) (int64, bool) {
	return aptSize(reAptNeed.FindStringSubmatch(line))
}

// AptGotBytes is one downloaded file's size from a "Get:" line.
func AptGotBytes(line string) (int64, bool) {
	return aptSize(reAptGet.FindStringSubmatch(line))
}

func aptSize(m []string) (int64, bool) {
	if m == nil {
		return 0, false
	}
	num := m[1]
	clean := make([]byte, 0, len(num))
	for i := 0; i < len(num); i++ {
		if num[i] != ',' { // a thousands separator in some locales
			clean = append(clean, num[i])
		}
	}
	v, err := strconv.ParseFloat(string(clean), 64)
	if err != nil {
		return 0, false
	}
	mult := map[string]float64{"B": 1, "kB": 1e3, "MB": 1e6, "GB": 1e9}[m[2]]
	return int64(v * mult), true
}
