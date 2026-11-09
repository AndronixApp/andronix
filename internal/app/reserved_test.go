package app

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

func TestReservedUserName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"shadow", true},          // #579 AX-WXCDVP-era: in the frozen list
		{"audio", true},           // #579 AX-WEAY2X: was already in the app list
		{"nogroup", true},         // android addition
		{"www-data", true},        // name with a hyphen
		{"_apt", true},            // name with a leading underscore
		{"systemd-network", true}, // systemd- prefix
		{"systemd-", true},        // bare prefix still reserved
		{"aid_inet", true},        // aid_ prefix (Android groups the rootfs carries)
		{"aid_", true},
		{"root", false}, // root has its own message in userNameProblem, not here
		{"alex", false},
		{"alex_2", false},
		{"ubuntu", false}, // a distro name, not a reserved account
		{"systemdx", false},
		{"aidx", false},
	}
	for _, c := range cases {
		if got := reservedUserName(c.name); got != c.want {
			t.Errorf("reservedUserName(%q) = %v, want %v", c.name, got, c.want)
		}
	}
}

// TestReservedNameListFrozen guards the list that must stay in sync with the
// Android app field (#579, app list 61562499): 102 unique, sorted names, with
// the known triggers present. A change here means sending android the delta.
func TestReservedNameListFrozen(t *testing.T) {
	if n := len(reservedNameList); n != 102 {
		t.Errorf("reservedNameList has %d names, want 102 (frozen with app list 61562499); send android the delta if this changed", n)
	}
	seen := map[string]bool{}
	for _, n := range reservedNameList {
		if seen[n] {
			t.Errorf("duplicate reserved name %q", n)
		}
		seen[n] = true
	}
	if len(seen) != len(reservedNameList) {
		t.Errorf("reservedNameList has duplicates")
	}
	if !sort.StringsAreSorted(reservedNameList) {
		t.Errorf("reservedNameList must stay sorted (matches the app file)")
	}
	for _, must := range []string{"shadow", "audio", "nogroup", "root"} {
		if !seen[must] {
			t.Errorf("reservedNameList is missing %q", must)
		}
	}
}

func TestUserNameProblemReserved(t *testing.T) {
	// Reserved names are well-formed, so userNameProblem must reject them on
	// the reserved rule, not a format rule.
	for _, n := range []string{"shadow", "audio", "www-data", "systemd-network", "aid_inet"} {
		if p := userNameProblem(n); p == "" {
			t.Errorf("userNameProblem(%q) = \"\", want a reserved-name problem", n)
		}
	}
	// root keeps its own dedicated message.
	if p := userNameProblem("root"); p == "" || p == "'root' is a reserved system name; pick another, e.g. alex" {
		t.Errorf("userNameProblem(root) = %q, want its own root message", p)
	}
	// A normal name is still fine.
	if p := userNameProblem("alex"); p != "" {
		t.Errorf("userNameProblem(alex) = %q, want \"\"", p)
	}
}

func TestUserNameTakenIn(t *testing.T) {
	root := t.TempDir()
	etc := filepath.Join(root, "etc")
	if err := os.MkdirAll(etc, 0o755); err != nil {
		t.Fatal(err)
	}
	passwd := "root:x:0:0:root:/root:/bin/bash\ntester:x:1000:1000::/home/tester:/bin/bash\n"
	group := "root:x:0:\nsudo:x:27:\naid_inet:x:3003:root,aid_tester\n"
	if err := os.WriteFile(filepath.Join(etc, "passwd"), []byte(passwd), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(etc, "group"), []byte(group), 0o644); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name  string
		taken bool
	}{
		{"tester", true},  // existing passwd user
		{"sudo", true},    // existing group only (not a passwd user) — the gap this closes
		{"inet", true},    // derived aid_inet group already exists → collision
		{"alex", false},   // free
		{"daemon", false}, // not in this rootfs (static list would catch it separately)
	}
	for _, c := range cases {
		if got := userNameTakenIn(root, c.name) != ""; got != c.taken {
			t.Errorf("userNameTakenIn(root, %q) taken=%v, want %v (reason=%q)", c.name, got, c.taken, userNameTakenIn(root, c.name))
		}
	}

	// A missing rootfs (nothing unpacked yet) reports nothing taken.
	if p := userNameTakenIn(t.TempDir(), "tester"); p != "" {
		t.Errorf("userNameTakenIn(empty root, tester) = %q, want \"\"", p)
	}
}
