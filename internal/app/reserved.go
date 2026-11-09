package app

import "strings"

// reservedNames are system user/group names the first-boot user can't take.
// Picking one makes useradd/groupadd fail at the very end of an install, after
// the whole rootfs is unpacked (#579: in-app users "shadow" and "audio"). We
// reject them up front instead — statically, before the rootfs even exists, so
// the app and the installer agree without either side reading /etc/passwd.
//
// This is a conservative superset: a name here may not exist on every distro,
// so it can refuse a name one distro would have accepted. That is accepted
// (the owner's call on #579); the per-rootfs passwd/group check in
// userNameTakenIn is the precise gate for anything distro-specific this list
// misses. Any name starting with "systemd-" or "aid_" is also reserved (see
// reservedUserName); "root" keeps its own message in userNameProblem.
var reservedNames = map[string]bool{}

func init() {
	for _, n := range reservedNameList {
		reservedNames[n] = true
	}
}

// reservedNameList is frozen with the Android app's username field so both
// sides refuse the same names. Edit here and send android the delta before
// their next AAB.
var reservedNameList = []string{
	// 102 reserved system user/group names, frozen with the Android app
	// list 61562499 (#579). Keep IN SYNC with the app field; send android any
	// additions before their next AAB. Built from the shipped images' passwd+group
	// (Ubuntu XFCE/KDE, Debian, Kali, Manjaro) + Debian base-passwd + common
	// Arch/Fedora/Alpine/Void defaults. Sorted.
	"_apt", "_ssh", "abuild", "adm", "at", "audio", "avahi", "backup",
	"bin", "bluetooth", "cdrom", "chrony", "clock", "colord", "cron", "crontab",
	"daemon", "dbus", "dialout", "dip", "disk", "fax", "floppy", "ftp",
	"games", "geoclue", "git", "gnats", "groups", "guest", "http", "input",
	"irc", "kmem", "kvm", "lightdm", "list", "lock", "log", "lp",
	"lpadmin", "mail", "man", "mem", "messagebus", "netdev", "network", "news",
	"nobody", "nogroup", "ntp", "operator", "optical", "ping", "pipewire", "plugdev",
	"polkitd", "postmaster", "power", "proc", "proxy", "pulse", "pulse-access", "rdma",
	"render", "rfkill", "root", "rpc", "rpcuser", "rtkit", "sambashare", "saned",
	"sasl", "scanner", "sddm", "sgx", "shadow", "smmsp", "src", "ssh_keys",
	"sshd", "ssl-cert", "staff", "storage", "sudo", "sync", "sys", "tape",
	"tss", "tty", "unbound", "usbmux", "users", "utempter", "utmp", "uucp",
	"uuidd", "video", "voice", "wheel", "www-data", "xfs",
}

// reservedUserName reports whether s is a reserved system name the first-boot
// user can't take: an exact match in reservedNames, or a "systemd-"/"aid_"
// prefix. "aid_" also covers the aid_<user> group the installer itself writes
// into the rootfs (configure.go), so a name that is already aid_* is out.
// "root" is deliberately not reported here — userNameProblem gives it its own
// message before this is reached.
func reservedUserName(s string) bool {
	if s == "root" {
		return false
	}
	if strings.HasPrefix(s, "systemd-") || strings.HasPrefix(s, "aid_") {
		return true
	}
	return reservedNames[s]
}
