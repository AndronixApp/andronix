package sys

import (
	"os"
	"strings"
)

// sveImplyingParts are ARM (implementer 0x41) CPU part numbers that LLVM's
// getHostCPUName maps to a core it believes has SVE2 (clang -mcpu defines
// __ARM_FEATURE_SVE2 for each). Qualcomm ships these with SVE turned off, so
// Mesa's llvmpipe JIT emits SVE instructions the CPU can't run and the first
// GL draw dies with SIGILL (#192: KDE black screen, glxgears 132). Keyed
// lowercase; extend this when a new SVE-core phone is confirmed (e.g. #912,
// 8 Elite / Oryon, once its part number is known).
var sveImplyingParts = map[string]bool{
	"0xd46": true, // Cortex-A510
	"0xd47": true, // Cortex-A710
	"0xd48": true, // Cortex-X2
	"0xd4d": true, // Cortex-A715
	"0xd4e": true, // Cortex-X3
	"0xd80": true, // Cortex-A520
	"0xd81": true, // Cortex-A720
	"0xd82": true, // Cortex-X4
	"0xd85": true, // Cortex-X925
	"0xd87": true, // Cortex-A725
}

// cpuinfoField returns the value of a "key : value" cpuinfo line, or "".
func cpuinfoField(line string) (key, val string, ok bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:]), true
}

// MaskCpuinfo rewrites a /proc/cpuinfo so Mesa's llvmpipe stops assuming SVE.
// When the file reports an ARM SVE-implying core (sveImplyingParts) but no
// "sve" in any Features line, every matching "CPU part" line is set to 0x000
// (LLVM then picks the generic target and keeps the JIT), and masked is true
// with the distinct parts it changed. Otherwise out == raw and masked is
// false. Pure, for tests; see MaskedCpuinfo for the live read.
func MaskCpuinfo(raw string) (out string, masked bool, parts []string) {
	hasSVE := false
	hasARM := false
	matched := map[string]bool{}
	for _, line := range strings.Split(raw, "\n") {
		k, v, ok := cpuinfoField(line)
		if !ok {
			continue
		}
		switch k {
		case "Features":
			for _, f := range strings.Fields(v) {
				if f == "sve" {
					hasSVE = true
				}
			}
		case "CPU implementer":
			if strings.EqualFold(v, "0x41") {
				hasARM = true
			}
		case "CPU part":
			if sveImplyingParts[strings.ToLower(v)] {
				matched[strings.ToLower(v)] = true
			}
		}
	}
	if hasSVE || !hasARM || len(matched) == 0 {
		return raw, false, nil
	}
	for p := range matched {
		parts = append(parts, p)
	}
	// Rewrite the matching "CPU part" lines to generic, preserving everything
	// else (core count, other fields) byte for byte.
	lines := strings.Split(raw, "\n")
	for i, line := range lines {
		k, v, ok := cpuinfoField(line)
		if ok && k == "CPU part" && sveImplyingParts[strings.ToLower(v)] {
			lines[i] = "CPU part\t: 0x000"
		}
	}
	return strings.Join(lines, "\n"), true, parts
}

// MaskedCpuinfo reads the host /proc/cpuinfo and, when the SVE-mask rule
// applies (see MaskCpuinfo), returns the rewritten content and ok=true.
func MaskedCpuinfo() (content string, ok bool) {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return "", false
	}
	out, masked, _ := MaskCpuinfo(string(b))
	return out, masked
}

// SVEMasked reports whether the host needs the SVE cpuinfo mask, and the
// distinct ARM part numbers it affects (for the compat probe / telemetry).
func SVEMasked() (masked bool, parts []string) {
	b, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return false, nil
	}
	_, masked, parts = MaskCpuinfo(string(b))
	return masked, parts
}
