package sys

import (
	"sort"
	"strings"
	"testing"
)

// A trimmed 8 Gen 1 cpuinfo: A510 (0xd46), A710 (0xd47), X2 (0xd48), ARM
// implementer, and no "sve" in Features.
const cpuinfo8Gen1 = `processor	: 0
BogoMIPS	: 38.40
Features	: fp asimd evtstrm aes pmull sha1 sha2 crc32 atomics fphp asimdhp
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x1
CPU part	: 0xd46
CPU revision	: 0

processor	: 3
BogoMIPS	: 38.40
Features	: fp asimd evtstrm aes pmull sha1 sha2 crc32 atomics fphp asimdhp
CPU implementer	: 0x41
CPU architecture: 8
CPU variant	: 0x1
CPU part	: 0xd47
CPU revision	: 0

processor	: 7
Features	: fp asimd evtstrm aes pmull sha1 sha2 crc32 atomics
CPU implementer	: 0x41
CPU part	: 0xd48
CPU revision	: 0
`

func TestMaskCpuinfoSVEOff(t *testing.T) {
	out, masked, parts := MaskCpuinfo(cpuinfo8Gen1)
	if !masked {
		t.Fatal("8 Gen 1 with no sve should be masked")
	}
	if strings.Contains(out, "0xd46") || strings.Contains(out, "0xd47") || strings.Contains(out, "0xd48") {
		t.Errorf("SVE-implying parts not rewritten:\n%s", out)
	}
	if n := strings.Count(out, "CPU part\t: 0x000"); n != 3 {
		t.Errorf("want 3 generic part lines, got %d:\n%s", n, out)
	}
	// Everything else is preserved: still three cores, Features untouched.
	if n := strings.Count(out, "processor\t:"); n != 3 {
		t.Errorf("core count changed: %d", n)
	}
	if !strings.Contains(out, "BogoMIPS\t: 38.40") || !strings.Contains(out, "CPU architecture: 8") {
		t.Errorf("non-part fields changed:\n%s", out)
	}
	sort.Strings(parts)
	if strings.Join(parts, ",") != "0xd46,0xd47,0xd48" {
		t.Errorf("parts = %v", parts)
	}
}

func TestMaskCpuinfoSVEOn(t *testing.T) {
	// Same parts but SVE is actually enabled: leave it alone.
	raw := strings.Replace(cpuinfo8Gen1, "atomics fphp asimdhp", "atomics sve sve2 fphp", 1)
	out, masked, parts := MaskCpuinfo(raw)
	if masked || parts != nil {
		t.Fatalf("sve present: want no mask, got masked=%v parts=%v", masked, parts)
	}
	if out != raw {
		t.Error("content changed although sve is present")
	}
}

func TestMaskCpuinfoNonARM(t *testing.T) {
	// A non-ARM implementer with a part number that collides must not match.
	raw := strings.ReplaceAll(cpuinfo8Gen1, "0x41", "0x51")
	_, masked, _ := MaskCpuinfo(raw)
	if masked {
		t.Error("non-ARM implementer should not be masked")
	}
}

func TestMaskCpuinfoNonSVECore(t *testing.T) {
	// Cortex-A78 (0xd41) / X1 (0xd44): LLVM doesn't assume SVE, so no mask.
	raw := strings.NewReplacer("0xd46", "0xd41", "0xd47", "0xd44", "0xd48", "0xd41").Replace(cpuinfo8Gen1)
	_, masked, _ := MaskCpuinfo(raw)
	if masked {
		t.Error("non-SVE cores should not be masked")
	}
}
