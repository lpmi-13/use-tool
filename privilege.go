package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// Kernel-log commands are written with a leading `sudo` because most
// distros restrict them to privileged users. Some hosts don't (a VM with
// kernel.dmesg_restrict=0, a user in the systemd-journal group, a root
// shell), so detectSystem probes what this user can actually read and the
// suggestions drop the `sudo` that isn't needed.

// canReadDmesg reports whether dmesg works without sudo. dmesg reads
// /dev/kmsg and falls back to syslog(2); the kernel applies the same
// dmesg_restrict / CAP_SYSLOG check to both.
func canReadDmesg() bool {
	if os.Geteuid() == 0 {
		return true
	}
	f, err := os.OpenFile("/dev/kmsg", os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err == nil {
		f.Close()
		return true
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	const syslogActionSizeBuffer = 10
	_, err = syscall.Klogctl(syslogActionSizeBuffer, nil)
	return err == nil
}

// canReadKernelJournal reports whether journalctl -k shows kernel messages
// without sudo. Those live in the system journal, which journald grants to
// root and, via ACLs, groups such as adm and systemd-journal. Without
// access journalctl still exits 0, printing only "-- No entries --".
func canReadKernelJournal() bool {
	if os.Geteuid() == 0 {
		return true
	}
	for _, dir := range []string{"/var/log/journal", "/run/log/journal"} {
		matches, _ := filepath.Glob(filepath.Join(dir, "*", "system*.journal"))
		for _, m := range matches {
			if syscall.Access(m, 4 /* R_OK */) == nil {
				return true
			}
		}
	}
	return false
}

// adaptSudo drops a leading `sudo` from a kernel-log command when this user
// can already read that log. Everything else is returned unchanged.
func (si SystemInfo) adaptSudo(cmd string) string {
	rest, ok := strings.CutPrefix(cmd, "sudo ")
	if !ok {
		return cmd
	}
	switch baseCmd(rest) {
	case "dmesg":
		if si.DmesgReadable {
			return rest
		}
	case "journalctl":
		if si.JournalReadable {
			return rest
		}
	}
	return cmd
}

// dmesgPermissionLine is dmesgPermissionNote on its own line, or nothing
// when this user can read dmesg without sudo.
func (si SystemInfo) dmesgPermissionLine() string {
	if si.DmesgReadable {
		return ""
	}
	return "\n" + dmesgPermissionNote
}

// forSystem returns ref with the privilege prefix and note appropriate for
// this host.
func (ref CommandRef) forSystem(si SystemInfo) CommandRef {
	ref.Cmd = si.adaptSudo(ref.Cmd)
	if si.DmesgReadable {
		ref.Summary = strings.TrimSuffix(ref.Summary, "\n"+dmesgPermissionNote)
	}
	if ref.Cmd == "iotop -bn1" && haveCmd("iotop") {
		_, needsSudo := iotopDirectStatus()
		if needsSudo && haveCmd("sudo") {
			ref.Cmd = "sudo iotop -bn1"
			ref.Summary += "\n" + iotopPermissionNote
		}
	}
	return ref
}
