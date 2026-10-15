package sys

import (
	"strings"
	"sync"
)

var (
	sdkOnce sync.Once
	sdk     int
)

// AndroidSDK is ro.build.version.sdk: 0 where it can't be read (off
// Android, or Termux's userland in a container on a PC).
func AndroidSDK() int {
	sdkOnce.Do(func() {
		if out, err := Command("getprop", "ro.build.version.sdk").Output(); err == nil {
			for _, c := range strings.TrimSpace(string(out)) {
				if c < '0' || c > '9' {
					sdk = 0
					return
				}
				sdk = sdk*10 + int(c-'0')
			}
		}
	})
	return sdk
}
