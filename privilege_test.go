package main

import (
	"strings"
	"testing"
)

func TestAdaptSudo(t *testing.T) {
	cases := []struct {
		si   SystemInfo
		cmd  string
		want string
	}{
		{SystemInfo{}, "sudo dmesg -T | tail", "sudo dmesg -T | tail"},
		{SystemInfo{DmesgReadable: true}, "sudo dmesg -T | tail", "dmesg -T | tail"},
		{SystemInfo{DmesgReadable: true}, "sudo journalctl -k -b", "sudo journalctl -k -b"},
		{SystemInfo{JournalReadable: true}, "sudo journalctl -k -b", "journalctl -k -b"},
		{SystemInfo{JournalReadable: true}, "sudo dmesg -T", "sudo dmesg -T"},
		{SystemInfo{DmesgReadable: true, JournalReadable: true}, "sudo ss -tlnp", "sudo ss -tlnp"},
		{SystemInfo{DmesgReadable: true}, "vmstat 1 3", "vmstat 1 3"},
	}
	for _, tc := range cases {
		if got := tc.si.adaptSudo(tc.cmd); got != tc.want {
			t.Errorf("%+v adaptSudo(%q) = %q, want %q", tc.si, tc.cmd, got, tc.want)
		}
	}
}

func TestGuideStepsDropSudoWhenKernelLogReadable(t *testing.T) {
	si := SystemInfo{HasJournalctl: true, DmesgReadable: true, JournalReadable: true}
	for _, inv := range []*Investigation{cpuInvestigation, memoryInvestigation, diskInvestigation, networkInvestigation} {
		for _, step := range inv.StepsFn(si) {
			if step.Dimension != "Errors" {
				continue
			}
			for _, cmd := range append([]string{step.Suggested}, step.Alternatives...) {
				if strings.HasPrefix(cmd, "sudo ") {
					t.Errorf("%s step %q suggests %q; want no sudo", inv.Name, step.Name, cmd)
				}
			}
			if strings.Contains(step.Intro, dmesgPermissionNote) {
				t.Errorf("%s step %q still carries the sudo note", inv.Name, step.Name)
			}
		}
	}
}

func TestGuideStepsKeepSudoByDefault(t *testing.T) {
	si := SystemInfo{HasJournalctl: true}
	for _, step := range cpuInvestigation.StepsFn(si) {
		if step.Name != "errors" {
			continue
		}
		if !strings.HasPrefix(step.Suggested, "sudo dmesg") {
			t.Fatalf("Suggested = %q, want sudo dmesg", step.Suggested)
		}
		if len(step.Alternatives) != 1 || !strings.HasPrefix(step.Alternatives[0], "sudo journalctl") {
			t.Fatalf("Alternatives = %q, want sudo journalctl", step.Alternatives)
		}
		if !strings.HasSuffix(step.Intro, "\n"+dmesgPermissionNote) {
			t.Fatalf("Intro lost the sudo note:\n%s", step.Intro)
		}
		return
	}
	t.Fatal("cpu errors step not found")
}

func TestCommandReferenceAdaptsSudoPerTool(t *testing.T) {
	out := captureStdout(func() {
		printCommands(cpuInvestigation, SystemInfo{HasJournalctl: true, DmesgReadable: true})
	})
	if strings.Contains(out, "sudo dmesg") || !strings.Contains(out, "dmesg --level=err,warn") {
		t.Fatalf("expected dmesg without sudo:\n%s", out)
	}
	if !strings.Contains(out, "sudo journalctl -k") {
		t.Fatalf("expected journalctl to keep sudo:\n%s", out)
	}
	if strings.Contains(out, "You will probably need sudo") {
		t.Fatalf("did not expect the dmesg sudo note:\n%s", out)
	}
}

func TestSuggestNextCommandsAdaptsSudo(t *testing.T) {
	got := suggestNextCommands(cpuInvestigation, "Errors", nil, SystemInfo{DmesgReadable: true}, 1)
	if len(got) != 1 || got[0].Cmd != "dmesg --level=err,warn | tail -30" {
		t.Fatalf("suggestNextCommands = %+v, want dmesg without sudo", got)
	}
}

func TestJournalctlUnprivilegedHintCountsAsFailure(t *testing.T) {
	out := "Hint: You are currently not seeing messages from other users and the system.\n" +
		"      Users in groups 'adm', 'systemd-journal' can see all messages.\n" +
		"      Pass -q to turn off this notice.\n-- No entries --\n"
	if !isJournalctlFailure("journalctl -k -b -p warning --no-pager -n 30", out) {
		t.Fatal("expected the unprivileged journalctl hint to count as a failure")
	}
}
