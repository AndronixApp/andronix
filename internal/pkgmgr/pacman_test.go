package pkgmgr

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AndronixApp/andronix-distros/internal/sys"
)

// The pacman command strings, as they run inside the distro.
func TestPacmanCommands(t *testing.T) {
	f, err := Get("pacman")
	if err != nil {
		t.Fatal(err)
	}
	const pac = "pacman --noconfirm --noprogressbar --color never "
	if f.Upgrade != pac+"-Su" {
		t.Errorf("Upgrade = %q", f.Upgrade)
	}
	if got, want := f.Install([]string{"sudo", "nano"}), pac+"-S --needed sudo nano"; got != want {
		t.Errorf("Install = %q, want %q", got, want)
	}
	const wantUpdate = `{ G=/etc/pacman.d/gnupg; b=; for c in /proc/[0-9]*/comm; do read -r n 2>/dev/null <"$c" && case $n in gpg|gpg-agent|gpgsm|dirmngr|keyboxd|pacman-key) b=1 ;; esac; done; ` +
		`[ -n "$b" ] || rm -f "$G"/*.lock "$G"/.#lk* "$G"/.l2s..#lk*; ` +
		`t=; { [ -e "$G"/.andronix-keyring-ok ] || [ -e "$G"/trustdb.gpg ]; } && gpg --homedir "$G" --batch --no-permission-warning --list-keys --with-colons 2>/dev/null | grep -q '^pub:f:' && t=1; ` +
		`{ [ -n "$t" ] && [ -e "$G"/.andronix-keyring-ok ]; } || ` +
		`{ rm -f /etc/pacman.d/gnupg/.andronix-keyring-ok; pacman-key --init && pacman-key --populate && touch /etc/pacman.d/gnupg/.andronix-keyring-ok; } || ` +
		`{ [ -n "$t" ] && echo "The package signing keys were not set up again; going on with the keys this install already trusts."; }; } && ` + pac + "-Syy"
	if f.Update != wantUpdate {
		t.Errorf("Update =\n%q, want\n%q", f.Update, wantUpdate)
	}
}

// Support #5349: "pubring.gpg exists" doesn't mean the keyring works. An
// install stopped during pacman-key leaves one whose keys nothing trusts,
// and Update must not take that for a finished keyring.
func TestPacmanUpdateKeyring(t *testing.T) {
	f, _ := Get("pacman")
	if strings.Contains(f.Update, "pubring.gpg") {
		t.Errorf("Update decides by pubring.gpg alone: %q", f.Update)
	}
	for _, bad := range []string{"--refresh-keys", "--recv-keys", "keyserver"} {
		if strings.Contains(f.Update+PacmanKeyringRebuild, bad) {
			t.Errorf("the keyring depends on a keyserver (%q)", bad)
		}
	}
	// The keyring comes before the sync, and a failed keyring stops it.
	k, s := strings.Index(f.Update, "pacman-key --init && pacman-key --populate"), strings.Index(f.Update, "-Syy")
	if k < 0 || s < k || !strings.Contains(f.Update, "} && pacman ") {
		t.Errorf("Update must make the keyring, then sync: %q", f.Update)
	}
	if !strings.Contains(PacmanKeyringRebuild, "pacman-key --init && pacman-key --populate && touch "+PacmanKeyringStamp) ||
		!strings.HasPrefix(PacmanKeyringRebuild, "{ rm -f "+PacmanKeyringStamp+";") {
		t.Errorf("PacmanKeyringRebuild = %q", PacmanKeyringRebuild)
	}
}

// The keyring step as a script: stale gpg locks go (a gpg killed mid-write
// leaves them, and gpg can wait on them for good), a keyring gpg reports no
// fully trusted key for is made again, and one that has such a key is left
// alone. gpg and pacman-key are stand-ins that record their calls.
func TestPacmanKeyringScript(t *testing.T) {
	f, _ := Get("pacman")
	keyring := strings.TrimSuffix(f.Update, " && "+"pacman --noconfirm --noprogressbar --color never -Syy")
	if keyring == f.Update {
		t.Fatalf("Update doesn't end with the sync: %q", f.Update)
	}
	root := t.TempDir()
	g := filepath.Join(root, "gnupg")
	bin := filepath.Join(root, "bin")
	os.MkdirAll(bin, 0o755)
	calls := filepath.Join(root, "calls")
	// gpg prints what ROOT/keys holds; pacman-key --populate makes the key trusted.
	gpgCalls := filepath.Join(root, "gpg-calls")
	os.WriteFile(filepath.Join(bin, "gpg"), []byte("#!/bin/sh\necho gpg >>"+gpgCalls+"\ncat "+root+"/keys 2>/dev/null\n"), 0o755)
	// While ROOT/fails names a step, that step fails as on a phone whose
	// gpg-agent won't start (support #119).
	os.WriteFile(filepath.Join(bin, "pacman-key"), []byte("#!/bin/sh\necho \"$*\" >>"+calls+"\n"+
		"[ \"$1\" = \"$(cat "+root+"/fails 2>/dev/null)\" ] && { echo 'gpg: agent_genkey failed: No agent running'; exit 1; }\n"+
		"[ \"$1\" = --populate ] && echo 'pub:f:4096:1:77193F152BDBE6A6:' >"+root+"/keys\nexit 0\n"), 0o755)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// The process list is made up too, so a gpg-agent on the machine
	// running the tests doesn't decide the outcome.
	proc := filepath.Join(root, "proc")
	script := strings.ReplaceAll(strings.ReplaceAll(keyring, "/etc/pacman.d/gnupg", g), "/proc/", proc+"/")
	if !strings.Contains(script, proc+"/[0-9]*/comm") {
		t.Fatalf("the keyring step doesn't look at /proc/<pid>/comm: %q", keyring)
	}
	running := func(names ...string) {
		os.RemoveAll(proc)
		for i, n := range names {
			d := filepath.Join(proc, []string{"101", "202", "303"}[i])
			os.MkdirAll(d, 0o755)
			os.WriteFile(filepath.Join(d, "comm"), []byte(n+"\n"), 0o644)
		}
	}
	running("sh", "pacman")
	run := func() {
		t.Helper()
		if out, err := sys.Command("/bin/sh", "-c", script).CombinedOutput(); err != nil {
			t.Fatalf("keyring step: %v\n%s", err, out)
		}
	}
	stamp := filepath.Join(g, ".andronix-keyring-ok")
	reset := func(keys string, stamped bool) {
		os.RemoveAll(g)
		os.MkdirAll(g, 0o700)
		os.Remove(calls)
		os.WriteFile(filepath.Join(root, "keys"), []byte(keys), 0o644)
		for _, l := range []string{"pubring.gpg.lock", ".#lk0x0000005500175200.host.66", ".l2s..#lk0x0000005500175200.host.660001"} {
			os.WriteFile(filepath.Join(g, l), []byte("66\n"), 0o644)
		}
		os.WriteFile(filepath.Join(g, "pubring.gpg"), []byte("keys"), 0o644)
		os.WriteFile(filepath.Join(g, "trustdb.gpg"), []byte("trust"), 0o644) // pacman-key --init made it
		if stamped {
			os.WriteFile(stamp, nil, 0o644)
		}
	}
	stamped := func() bool { _, err := os.Stat(stamp); return err == nil }
	locks := func() (n int) {
		es, _ := os.ReadDir(g)
		for _, e := range es {
			if strings.Contains(e.Name(), "lk") || strings.HasSuffix(e.Name(), ".lock") {
				n++
			}
		}
		return n
	}

	// Stopped after --init: only the local master key (ultimate, not full).
	reset("pub:u:4096:1:AAAAAAAAAAAAAAAA:\n", false)
	run()
	if b, _ := os.ReadFile(calls); string(b) != "--init\n--populate\n" {
		t.Errorf("half-made keyring: pacman-key calls %q, want --init then --populate", b)
	}
	if locks() != 0 {
		t.Errorf("%d stale gpg lock files left", locks())
	}
	if _, err := os.Stat(filepath.Join(g, "pubring.gpg")); err != nil {
		t.Error("the keyring itself was removed")
	}
	if !stamped() {
		t.Error("finished --populate left no stamp")
	}

	// Stopped inside --populate: keys imported, none signed.
	reset("pub:u:4096:1:AAAAAAAAAAAAAAAA:\npub:-:4096:1:77193F152BDBE6A6:\n", false)
	run()
	if b, _ := os.ReadFile(calls); string(b) != "--init\n--populate\n" {
		t.Errorf("unsigned keys: pacman-key calls %q, want --init then --populate", b)
	}

	// Stopped inside --populate after one key was signed: a fully trusted
	// key but no stamp, so --populate runs again.
	reset("pub:u:4096:1:AAAAAAAAAAAAAAAA:\npub:f:4096:1:77193F152BDBE6A6:\npub:-:4096:1:BBBBBBBBBBBBBBBB:\n", false)
	run()
	if b, _ := os.ReadFile(calls); string(b) != "--init\n--populate\n" {
		t.Errorf("half-signed keyring: pacman-key calls %q, want --init then --populate", b)
	}

	// A stamp but gpg trusts nothing (keyring replaced or damaged): made
	// again.
	reset("pub:u:4096:1:AAAAAAAAAAAAAAAA:\n", true)
	run()
	if b, _ := os.ReadFile(calls); string(b) != "--init\n--populate\n" {
		t.Errorf("stamped but untrusted: pacman-key calls %q, want --init then --populate", b)
	}

	// A gpg program is running (a pacman-key of the user's own): its locks
	// are live, so they stay.
	reset("pub:u:4096:1:AAAAAAAAAAAAAAAA:\npub:f:4096:1:77193F152BDBE6A6:\n", true)
	running("sh", "gpg-agent")
	run()
	if locks() != 3 {
		t.Errorf("%d of 3 lock files left while gpg-agent runs", locks())
	}
	running("sh")

	// A working keyring: nothing is made again.
	reset("pub:u:4096:1:AAAAAAAAAAAAAAAA:\npub:f:4096:1:77193F152BDBE6A6:\n", true)
	run()
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("working keyring: pacman-key ran: %q", b)
	}

	// An install from before the stamp (2.1.5): trusted keys, no stamp. Its
	// one more --populate fails where gpg-agent won't start, at either
	// step; the keyring step must still pass, so the sync goes on with the
	// keys the install has, and leave no stamp, so a later update tries
	// again (review of rc-2.1.6).
	fails := filepath.Join(root, "fails")
	defer os.Remove(fails)
	for _, c := range []struct{ step, calls string }{{"--populate", "--init\n--populate\n"}, {"--init", "--init\n"}} {
		os.WriteFile(fails, []byte(c.step), 0o644)
		reset("pub:u:4096:1:AAAAAAAAAAAAAAAA:\npub:f:4096:1:77193F152BDBE6A6:\n", false)
		out, err := sys.Command("/bin/sh", "-c", script).CombinedOutput()
		if err != nil {
			t.Errorf("%s fails on a keyring with trusted keys: the keyring step failed too: %v\n%s", c.step, err, out)
		}
		if b, _ := os.ReadFile(calls); string(b) != c.calls {
			t.Errorf("%s fails: pacman-key calls %q, want %q", c.step, b, c.calls)
		}
		if stamped() {
			t.Errorf("%s failed, yet the keyring is stamped finished", c.step)
		}
		if !strings.Contains(string(out), keyringKeptNote) {
			t.Errorf("%s fails: nothing says the keys were kept:\n%s", c.step, out)
		}
		if _, err := os.Stat(filepath.Join(g, "pubring.gpg")); err != nil {
			t.Errorf("%s fails: the keyring was removed", c.step)
		}
	}
	// With no key pacman trusts there is nothing to go on with: a failed
	// keyring stops the update, as before (stopped mid --populate, stamped
	// or not).
	for _, c := range []struct {
		keys    string
		stamped bool
	}{{"pub:u:4096:1:AAAAAAAAAAAAAAAA:\n", false}, {"pub:u:4096:1:AAAAAAAAAAAAAAAA:\npub:-:4096:1:77193F152BDBE6A6:\n", false}, {"", true}} {
		os.WriteFile(fails, []byte("--populate"), 0o644)
		reset(c.keys, c.stamped)
		if out, err := sys.Command("/bin/sh", "-c", script).CombinedOutput(); err == nil {
			t.Errorf("keys %q: --populate failed and the keyring step passed:\n%s", c.keys, out)
		} else if strings.Contains(string(out), keyringKeptNote) {
			t.Errorf("keys %q: claims keys were kept:\n%s", c.keys, out)
		}
	}
	// gpg is not asked about a directory with no keyring in it (the retry
	// code's keyringReset leaves an empty one; a pacman-key --init killed at
	// its start, an empty pubring.gpg): it would make a pubring.kbx there,
	// and the keys would then go into that instead of pubring.gpg. Both
	// steps just run.
	os.Remove(fails)
	for _, empty := range []bool{true, false} {
		reset("pub:u:4096:1:AAAAAAAAAAAAAAAA:\npub:f:4096:1:77193F152BDBE6A6:\n", false)
		os.Remove(filepath.Join(g, "pubring.gpg"))
		os.Remove(filepath.Join(g, "trustdb.gpg"))
		if !empty {
			os.WriteFile(filepath.Join(g, "pubring.gpg"), nil, 0o644)
		}
		os.Remove(gpgCalls)
		run()
		if b, _ := os.ReadFile(gpgCalls); len(b) != 0 {
			t.Errorf("no keyring yet (empty dir: %v): gpg was run on it", empty)
		}
		if b, _ := os.ReadFile(calls); string(b) != "--init\n--populate\n" || !stamped() {
			t.Errorf("no keyring yet (empty dir: %v): pacman-key calls %q, stamped %v", empty, b, stamped())
		}
	}
	// A stamped keyring is asked as before, whatever files it is made of.
	reset("pub:u:4096:1:AAAAAAAAAAAAAAAA:\npub:f:4096:1:77193F152BDBE6A6:\n", true)
	os.Remove(filepath.Join(g, "pubring.gpg"))
	os.Remove(filepath.Join(g, "trustdb.gpg"))
	run()
	if b, _ := os.ReadFile(calls); len(b) != 0 {
		t.Errorf("stamped keyring without pubring.gpg: pacman-key ran: %q", b)
	}

	// What the step prints when it keeps the keys isn't read as a failure
	// of the run it is part of.
	if f := Diagnose([]string{keyringKeptNote}, errors.New("exit status 1")); f.Kind != KindOther {
		t.Errorf("the kept-keys note is diagnosed as %s", f.Kind)
	}
}

// Support #5412: the repair reinstalls half-installed packages by name,
// over their half-unpacked files, and never ends the shell it runs in.
func TestPacmanRepairCommand(t *testing.T) {
	f, _ := Get("pacman")
	if f.Repair != PacmanRepair || f.Repair == "" {
		t.Fatalf("pacman Repair = %q", f.Repair)
	}
	for _, want := range []string{
		`for d in "$L"/*/; do`,
		`[ -s "$d/desc" ] && [ -e "$d/files" ] && continue`,
		"pacman --noconfirm --noprogressbar --color never -S --overwrite '*' $p && rm -f \"$T\"",
	} {
		if !strings.Contains(f.Repair, want) {
			t.Errorf("Repair misses %q:\n%s", want, f.Repair)
		}
	}
	for _, l := range strings.Split(f.Repair, "\n") {
		if w := strings.Fields(l); len(w) > 0 && (w[0] == "exit" || strings.Contains(l, " exit ")) {
			t.Errorf("Repair must not exit (it runs wrapped): %q", l)
		}
	}
	for _, id := range []string{"apt", "dnf", "apk", "xbps"} {
		if o, err := Get(id); err == nil && o.Repair != "" {
			t.Errorf("%s has a Repair: %q", id, o.Repair)
		}
	}
}
