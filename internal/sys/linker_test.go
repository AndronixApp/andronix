package sys

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLinkerExec(t *testing.T) {
	d := t.TempDir()
	elf := filepath.Join(d, "proot")
	os.WriteFile(elf, []byte("\x7fELF rest"), 0o755)
	script := filepath.Join(d, "termux-wake-lock")
	os.WriteFile(script, []byte("#!/bin/sh -e\necho hi\n"), 0o755)
	sysScript := filepath.Join(d, "sys")
	os.WriteFile(sysScript, []byte("#!/system/bin/sh\necho hi\n"), 0o755)
	t.Setenv("PREFIX", d+"/com.termux/usr")
	defer func(a string) { appData, linkerPath, selfPath = a, "", "" }(appData)
	appData = d + "/"

	// Started directly: nothing changes.
	args := []string{"andronix", "version"}
	if got := fixLinkerArgs("/data/x/andronix", "", args); !reflect.DeepEqual(got, args) || linkerPath != "" {
		t.Fatalf("direct: %v %q", got, linkerPath)
	}
	if p, a, _ := viaLinker(elf, []string{"proot", "-0"}); p != elf || len(a) != 2 {
		t.Errorf("direct exec rewritten: %s %v", p, a)
	}

	// Through the linker: our path is dropped from the arguments.
	got := fixLinkerArgs("/system/bin/linker64", elf, []string{"andronix", elf, "version"})
	if !reflect.DeepEqual(got, []string{"andronix", "version"}) || selfPath != elf {
		t.Fatalf("linker: %v %q", got, selfPath)
	}
	// Run from another place or name: TERMUX_EXEC__PROC_SELF_EXE names the
	// file differently (Redmi, rc4 as ./rc4/andronix), still dropped.
	got = fixLinkerArgs("/system/bin/linker64", "rc4/andronix", []string{"andronix", elf, "install", "alpine"})
	if !reflect.DeepEqual(got, []string{"andronix", "install", "alpine"}) {
		t.Fatalf("linker, other name: %v", got)
	}
	// A cgo build: bionic already dropped the path, argv[0] is it.
	selfPath = ""
	got = fixLinkerArgs("/system/bin/linker64", "", []string{elf, "__dnsfwd", "/r", "1"})
	if !reflect.DeepEqual(got, []string{elf, "__dnsfwd", "/r", "1"}) || selfPath != elf {
		t.Fatalf("cgo, argv[0] is ours: %v %q", got, selfPath)
	}
	selfPath = ""
	got = fixLinkerArgs("/system/bin/linker64", elf, []string{"andronix", "version"})
	if !reflect.DeepEqual(got, []string{"andronix", "version"}) || selfPath != elf {
		t.Fatalf("cgo, argv[0] a bare name: %v %q", got, selfPath)
	}
	got = fixLinkerArgs("/system/bin/linker64", elf, []string{"andronix", elf, "version"})
	if p, _ := Executable(); p != elf {
		t.Errorf("Executable = %s", p)
	}
	p, a, self := viaLinker(elf, []string{"proot", "-0", "sh"})
	if p != "/system/bin/linker64" || !reflect.DeepEqual(a, []string{"proot", elf, "-0", "sh"}) || self != elf {
		t.Errorf("elf: %s %v %s", p, a, self)
	}
	wantInterp := d + "/com.termux/usr/bin/sh"
	p, a, self = viaLinker(script, []string{"termux-wake-lock", "x"})
	if p != "/system/bin/linker64" || !reflect.DeepEqual(a, []string{"/bin/sh", wantInterp, "-e", script, "x"}) || self != wantInterp {
		t.Errorf("script: %s %v %s", p, a, self)
	}
	p, a, _ = viaLinker(sysScript, []string{"sys"})
	if p != "/system/bin/sh" || !reflect.DeepEqual(a, []string{"/system/bin/sh", sysScript}) {
		t.Errorf("system script: %s %v", p, a)
	}
	if p, _, _ := viaLinker("/system/bin/getprop", []string{"getprop"}); p != "/system/bin/getprop" {
		t.Errorf("system binary rewritten: %s", p)
	}
}

// The app's nativeLibraryDir unpacked 0644 (#229): through the linker, a
// readable ELF there is runnable, so PROOT_BIN isn't "missing".
func TestLinkerNoExecBit(t *testing.T) {
	d := t.TempDir()
	elf := filepath.Join(d, "libproot.so")
	os.WriteFile(elf, []byte("\x7fELF rest"), 0o644)
	text := filepath.Join(d, "notes")
	os.WriteFile(text, []byte("hi"), 0o644)
	defer func(a string) { appData, linkerPath, selfPath = a, "", "" }(appData)
	appData = d + "/"

	linkerPath = ""
	if _, err := LookPath(elf); err == nil {
		t.Errorf("started directly: 0644 ELF found")
	}
	linkerPath = "/system/bin/linker64"
	if p, err := LookPath(elf); err != nil || p != elf {
		t.Errorf("through the linker: %q %v", p, err)
	}
	if _, err := LookPath(text); err == nil {
		t.Errorf("0644 non-ELF found")
	}
	appData = "/elsewhere/"
	if _, err := LookPath(elf); err == nil {
		t.Errorf("0644 ELF outside app data found")
	}
}
