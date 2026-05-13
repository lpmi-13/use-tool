package main

import (
	"bufio"
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestSuggestCommand(t *testing.T) {
	got, ok := suggestCommand("practce", topLevelCommands)
	if !ok {
		t.Fatal("expected a suggestion")
	}
	if got != "practice" {
		t.Fatalf("suggestCommand() = %q, want %q", got, "practice")
	}
}

func TestSuggestCommandTooDistant(t *testing.T) {
	if got, ok := suggestCommand("xyz", topLevelCommands); ok {
		t.Fatalf("suggestCommand() = %q, want no suggestion", got)
	}
}

func TestCommandStatusMarksUnavailableRequirements(t *testing.T) {
	ref := CommandRef{
		Cmd:      "mpstat -P ALL 1 N",
		Requires: []string{"mpstat"},
	}
	got := commandStatus(ref, SystemInfo{})
	want := "unavailable: mpstat not found; install sysstat"
	if got != want {
		t.Fatalf("commandStatus() = %q, want %q", got, want)
	}
}

func TestCommandStatusEmptyWhenRequirementsAvailable(t *testing.T) {
	ref := CommandRef{
		Cmd:      "mpstat -P ALL 1 N",
		Requires: []string{"mpstat", "psi"},
	}
	si := SystemInfo{HasMpstat: true, HasPSI: true}
	if got := commandStatus(ref, si); got != "" {
		t.Fatalf("commandStatus() = %q, want empty status", got)
	}
}

func TestCommandStatusUsesDetectedJournalctlAvailability(t *testing.T) {
	ref := CommandRef{
		Cmd:      "journalctl -k -b --no-pager -n 30",
		Requires: []string{"journalctl"},
	}
	if got := commandStatus(ref, SystemInfo{}); got != "unavailable: journalctl not found" {
		t.Fatalf("commandStatus() = %q, want unavailable journalctl", got)
	}
	if got := commandStatus(ref, SystemInfo{HasJournalctl: true}); got != "" {
		t.Fatalf("commandStatus() = %q, want empty status", got)
	}
}

func TestCommandReferenceHidesJournalctlWhenUnavailable(t *testing.T) {
	withoutJournal := captureStdout(func() {
		printCommands(cpuInvestigation, SystemInfo{})
	})
	if strings.Contains(withoutJournal, "journalctl") {
		t.Fatalf("did not expect journalctl command when unavailable:\n%s", withoutJournal)
	}

	withJournal := captureStdout(func() {
		printCommands(cpuInvestigation, SystemInfo{HasJournalctl: true})
	})
	if !strings.Contains(withJournal, "journalctl -k -b") {
		t.Fatalf("expected journalctl command when available:\n%s", withJournal)
	}
}

func TestIsExitCommand(t *testing.T) {
	for _, input := range []string{"exit", "quit", " EXIT ", "Quit"} {
		if !isExitCommand(input) {
			t.Fatalf("isExitCommand(%q) = false, want true", input)
		}
	}
	for _, input := range []string{"", "1", "exiting", "q"} {
		if isExitCommand(input) {
			t.Fatalf("isExitCommand(%q) = true, want false", input)
		}
	}
}

func TestStripCopiedShellPrompt(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"$ cat /proc/pressure/memory", "cat /proc/pressure/memory", true},
		{"  $   ps -eo pid,rss,comm  ", "ps -eo pid,rss,comm", true},
		{"$", "", true},
		{"cat /proc/meminfo", "cat /proc/meminfo", false},
		{"$HOME/bin/tool", "$HOME/bin/tool", false},
	}
	for _, tc := range cases {
		got, ok := stripCopiedShellPrompt(tc.input)
		if got != tc.want || ok != tc.ok {
			t.Errorf("stripCopiedShellPrompt(%q) = %q, %v; want %q, %v", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAskQuestionRunsPromptCommandThenAcceptsAnswer(t *testing.T) {
	oldStdin := stdin
	defer func() { stdin = oldStdin }()
	stdin = bufio.NewReader(strings.NewReader("$ cat /proc/pressure/memory\n1\n"))

	var ran []string
	result := askQuestionWithCommandRunner(Question{
		Stem:    "Which answer?",
		Correct: "the only option",
	}, func(cmd string) CapturedCommand {
		ran = append(ran, cmd)
		return CapturedCommand{Cmd: cmd, Output: "some avg10=0.00\n"}
	})

	if !result.Correct || result.Quit {
		t.Fatalf("askQuestionWithCommandRunner() = %+v, want correct non-quit result", result)
	}
	if len(ran) != 1 || ran[0] != "cat /proc/pressure/memory" {
		t.Fatalf("runner commands = %v, want [cat /proc/pressure/memory]", ran)
	}
}

func TestRunAndCaptureSkipsFailedCommand(t *testing.T) {
	s := &Session{}
	c := s.runAndCapture("echo 'unterminated")

	if !c.Failed {
		t.Fatal("expected command to be marked failed")
	}
	if len(s.Captured) != 0 {
		t.Fatalf("captured failed command: %+v", s.Captured)
	}
}

func TestRunAndCaptureTreatsDmesgPermissionErrorInPipelineAsFailed(t *testing.T) {
	s := &Session{}
	var c CapturedCommand
	stderr := captureStderr(func() {
		c = s.runAndCapture("printf 'dmesg: read kernel buffer failed: Operation not permitted\\n' >&2 | true")
	})

	if !c.Failed {
		t.Fatalf("expected dmesg permission error to be marked failed, got %+v", c)
	}
	if c.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", c.ExitCode)
	}
	if len(s.Captured) != 0 {
		t.Fatalf("captured failed command: %+v", s.Captured)
	}
	if !strings.Contains(stderr, "retry with sudo") {
		t.Fatalf("expected retry hint in stderr, got:\n%s", stderr)
	}
}

func TestDmesgPermissionFailureMessageMentionsJournalctlOnlyWhenAvailable(t *testing.T) {
	withJournal := dmesgPermissionFailureMessage(true)
	if !strings.Contains(withJournal, "journalctl -k") {
		t.Fatalf("expected journalctl hint when available, got %q", withJournal)
	}

	withoutJournal := dmesgPermissionFailureMessage(false)
	if strings.Contains(withoutJournal, "journalctl") {
		t.Fatalf("did not expect journalctl hint when unavailable, got %q", withoutJournal)
	}
	if !strings.Contains(withoutJournal, "sudo") {
		t.Fatalf("expected sudo hint, got %q", withoutJournal)
	}
}

func TestRunAndCaptureTreatsJournalctlFailureInPipelineAsFailed(t *testing.T) {
	s := &Session{}
	var c CapturedCommand
	stderr := captureStderr(func() {
		c = s.runAndCapture("journalctl() { printf 'No journal files were opened due to insufficient permissions.\\n' >&2; }; journalctl | true")
	})

	if !c.Failed {
		t.Fatalf("expected journalctl access error to be marked failed, got %+v", c)
	}
	if c.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", c.ExitCode)
	}
	if len(s.Captured) != 0 {
		t.Fatalf("captured failed command: %+v", s.Captured)
	}
	if !strings.Contains(stderr, "journalctl could not read the kernel log") {
		t.Fatalf("expected journalctl retry hint in stderr, got:\n%s", stderr)
	}
}

func TestGuideStepCommandRetriesFailedAcceptAnyCommand(t *testing.T) {
	oldStdin := stdin
	defer func() { stdin = oldStdin }()
	stdin = bufio.NewReader(strings.NewReader("echo 'unterminated\necho ok\n"))

	s := &Session{}
	captured := guideStepCommand(s, GuideStep{AcceptAny: true, Suggested: "echo ok"})
	if captured == nil {
		t.Fatal("expected successful retry to be captured")
	}
	if captured.Cmd != "echo ok" || captured.Failed {
		t.Fatalf("captured = %+v, want successful echo retry", captured)
	}
	if len(s.Captured) != 1 || s.Captured[0].Cmd != "echo ok" {
		t.Fatalf("session captured = %+v, want only successful retry", s.Captured)
	}
}

func TestGuideQuestionsNilSafe(t *testing.T) {
	got := guideQuestions(SystemInfo{}, GuideStep{}, CapturedCommand{Cmd: "lsblk", Output: "NAME TYPE\n"})
	if got != nil {
		t.Fatalf("guideQuestions with nil QuestionsFn = %v, want nil", got)
	}
}

func TestDiskDeviceStepHasLsblkQuestions(t *testing.T) {
	steps := diskSteps(SystemInfo{})
	if len(steps) == 0 {
		t.Fatal("expected disk guide steps")
	}
	if steps[0].Name != "devices" {
		t.Fatalf("first disk step = %q, want devices", steps[0].Name)
	}
	qs := guideQuestions(SystemInfo{}, steps[0], CapturedCommand{
		Cmd:    "lsblk",
		Output: "NAME MAJ:MIN RM SIZE RO TYPE MOUNTPOINTS\nnvme0n1 259:0 0 238.5G 0 disk\nnvme0n1p1 259:1 0 512M 0 part /boot/efi\n",
	})
	if len(qs) == 0 {
		t.Fatal("expected lsblk questions for disk devices step")
	}
}

func TestGuideStepCommandPrintsEmptyOutputMessage(t *testing.T) {
	oldStdin := stdin
	defer func() { stdin = oldStdin }()
	stdin = bufio.NewReader(strings.NewReader("printf ''\n\n"))

	out := captureStdout(func() {
		captured := guideStepCommand(&Session{}, GuideStep{
			Suggested:          "printf ''",
			AcceptAny:          true,
			EmptyOutputMessage: "No matching errors found.",
		})
		if captured == nil {
			t.Fatal("expected command to be captured")
		}
	})
	if !strings.Contains(out, "No matching errors found.") {
		t.Fatalf("expected empty-output message in output:\n%s", out)
	}
	if !strings.Contains(out, "Press Enter to continue...") {
		t.Fatalf("expected pause prompt after empty-output message:\n%s", out)
	}
}

func TestErrorGuideStepsHaveEmptyOutputMessages(t *testing.T) {
	cases := []struct {
		resource string
		steps    []GuideStep
	}{
		{"cpu", cpuSteps(SystemInfo{})},
		{"memory", memorySteps(SystemInfo{HasPSI: true})},
		{"disk", diskSteps(SystemInfo{HasPSI: true})},
		{"network", networkSteps(SystemInfo{})},
	}
	for _, tc := range cases {
		step, ok := findGuideStep(tc.steps, "errors")
		if !ok {
			t.Fatalf("%s: expected errors step", tc.resource)
		}
		if step.EmptyOutputMessage == "" {
			t.Fatalf("%s: expected empty-output message for errors step", tc.resource)
		}
	}
}

func TestErrorGuideStepsOfferJournalctlAlternativeOnlyWhenAvailable(t *testing.T) {
	cases := []struct {
		resource string
		stepsFn  func(SystemInfo) []GuideStep
	}{
		{"cpu", cpuSteps},
		{"memory", memorySteps},
		{"disk", diskSteps},
		{"network", networkSteps},
	}
	for _, tc := range cases {
		withJournal, ok := findGuideStep(tc.stepsFn(SystemInfo{HasPSI: true, HasJournalctl: true}), "errors")
		if !ok {
			t.Fatalf("%s: expected errors step", tc.resource)
		}
		if len(withJournal.Alternatives) != 1 || !strings.Contains(withJournal.Alternatives[0], "journalctl -k -b") {
			t.Fatalf("%s: expected one journalctl alternative, got %#v", tc.resource, withJournal.Alternatives)
		}
		if strings.Contains(withJournal.Intro, "journalctl") {
			t.Fatalf("%s: intro should not unconditionally mention journalctl: %s", tc.resource, withJournal.Intro)
		}

		withoutJournal, ok := findGuideStep(tc.stepsFn(SystemInfo{HasPSI: true}), "errors")
		if !ok {
			t.Fatalf("%s: expected errors step", tc.resource)
		}
		if len(withoutJournal.Alternatives) != 0 {
			t.Fatalf("%s: did not expect journalctl alternative when unavailable, got %#v", tc.resource, withoutJournal.Alternatives)
		}
	}
}

func TestGuideStepHeaderPrintsAlternativesCompactly(t *testing.T) {
	out := captureStdout(func() {
		printGuideStepHeader(1, 2, GuideStep{
			Name:         "errors",
			Intro:        "Intro text.",
			Suggested:    "dmesg -T | tail",
			Alternatives: []string{"journalctl -k -b --no-pager -n 30"},
		})
	})
	if !strings.Contains(out, "Suggested: dmesg -T | tail") {
		t.Fatalf("expected suggested command in output:\n%s", out)
	}
	if !strings.Contains(out, "Alternative: journalctl -k -b --no-pager -n 30") {
		t.Fatalf("expected compact alternative line in output:\n%s", out)
	}
}

func TestDmesgRecommendationsDoNotDiscardPermissionErrors(t *testing.T) {
	for _, inv := range investigations {
		step, ok := findGuideStep(inv.StepsFn(SystemInfo{HasPSI: true}), "errors")
		if ok && strings.Contains(step.Suggested, "dmesg") && strings.Contains(step.Suggested, "2>/dev/null") {
			t.Fatalf("%s guide suggests dmesg command that discards stderr: %s", inv.Name, step.Suggested)
		}
		if ok && strings.Contains(step.Suggested, "dmesg") && !mentionsDmesgPermission(step.Intro) {
			t.Fatalf("%s guide dmesg step does not mention sudo/kernel-buffer access: %s", inv.Name, step.Intro)
		}
		for _, ref := range inv.Commands {
			if strings.Contains(ref.Cmd, "dmesg") && strings.Contains(ref.Cmd, "2>/dev/null") {
				t.Fatalf("%s command reference discards dmesg stderr: %s", inv.Name, ref.Cmd)
			}
			if strings.Contains(ref.Cmd, "dmesg") && !mentionsDmesgPermission(ref.Summary) {
				t.Fatalf("%s command reference does not mention sudo/kernel-buffer access: %s", inv.Name, ref.Cmd)
			}
		}
	}
}

func mentionsDmesgPermission(text string) bool {
	low := strings.ToLower(text)
	return strings.Contains(low, "kernel buffer") && strings.Contains(low, "sudo")
}

func findGuideStep(steps []GuideStep, name string) (GuideStep, bool) {
	for _, step := range steps {
		if step.Name == name {
			return step, true
		}
	}
	return GuideStep{}, false
}

func captureStderr(fn func()) string {
	old := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		panic(err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = old
	var buf bytes.Buffer
	buf.ReadFrom(r)
	r.Close()
	return buf.String()
}
