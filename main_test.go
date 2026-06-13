package main

import (
	"bufio"
	"bytes"
	"math/rand"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
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
		Requires: []string{"mpstat", "psi-cpu"},
	}
	si := SystemInfo{HasMpstat: true, HasPSI: true}
	if got := commandStatus(ref, si); got != "" {
		t.Fatalf("commandStatus() = %q, want empty status", got)
	}
}

func TestCommandStatusDistinguishesPSIResources(t *testing.T) {
	ref := CommandRef{
		Cmd:      "cat /proc/pressure/memory",
		Requires: []string{"psi-memory"},
	}
	if got := commandStatus(ref, SystemInfo{HasPSI: true}); got == "" {
		t.Fatal("memory PSI should not be available just because CPU PSI is available")
	}
	if got := commandStatus(ref, SystemInfo{HasMemoryPSI: true}); got != "" {
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

func TestReadRawLineCtrlCClearsNonEmptyLine(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("vmstat 1 3\x03mpstat\n"))
	var out bytes.Buffer

	line, status := readRawLine("[practice] $ ", &out, reader.ReadByte)
	if status != lineReadOK || line != "mpstat" {
		t.Fatalf("readRawLine() = %q, %v; want mpstat, lineReadOK", line, status)
	}
	got := out.String()
	if !strings.Contains(got, "\r\x1b[K[practice] $ ") {
		t.Fatalf("expected ctrl-c to clear and redraw the prompt, got %q", got)
	}
	if !strings.HasSuffix(got, "mpstat\n") {
		t.Fatalf("expected replacement command to be echoed, got %q", got)
	}
}

func TestReadRawLineCtrlCOnEmptyLineInterrupts(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\x03"))
	var out bytes.Buffer

	line, status := readRawLine("[practice] $ ", &out, reader.ReadByte)
	if status != lineReadInterrupted || line != "" {
		t.Fatalf("readRawLine() = %q, %v; want empty, lineReadInterrupted", line, status)
	}
	if got := out.String(); got != "^C\n" {
		t.Fatalf("ctrl-c output = %q, want %q", got, "^C\n")
	}
}

func TestReadRawLineBackspaceEditsInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("vmstatx\x7f 1\n"))
	var out bytes.Buffer

	line, status := readRawLine("[practice] $ ", &out, reader.ReadByte)
	if status != lineReadOK || line != "vmstat 1" {
		t.Fatalf("readRawLine() = %q, %v; want vmstat 1, lineReadOK", line, status)
	}
	if got := out.String(); !strings.Contains(got, "\b \b") {
		t.Fatalf("expected backspace erase sequence, got %q", got)
	}
}

func TestReadRawLineCtrlLClearsScreenAndKeepsInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("vm\x0cstat\n"))
	var out bytes.Buffer

	line, status := readRawLine("[practice] $ ", &out, reader.ReadByte)
	if status != lineReadOK || line != "vmstat" {
		t.Fatalf("readRawLine() = %q, %v; want vmstat, lineReadOK", line, status)
	}
	got := out.String()
	if !strings.Contains(got, "\x1b[H\x1b[2J[practice] $ vm") {
		t.Fatalf("expected ctrl-l to clear screen and redraw current input, got %q", got)
	}
	if strings.Contains(got, "\x0c") {
		t.Fatalf("ctrl-l leaked into output: %q", got)
	}
}

func TestReadRawLineIgnoresOtherControlBytes(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("d\x01d\n"))
	var out bytes.Buffer

	line, status := readRawLine("[practice] $ ", &out, reader.ReadByte)
	if status != lineReadOK || line != "dd" {
		t.Fatalf("readRawLine() = %q, %v; want dd, lineReadOK", line, status)
	}
	if got := out.String(); strings.Contains(got, "\x01") {
		t.Fatalf("control byte leaked into output: %q", got)
	}
}

func TestReadRawLineConsumesEscapeSequences(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("\x1b[Aecho\x1b]0;title\x07 ok\n"))
	var out bytes.Buffer

	line, status := readRawLine("[practice] $ ", &out, reader.ReadByte)
	if status != lineReadOK || line != "echo ok" {
		t.Fatalf("readRawLine() = %q, %v; want echo ok, lineReadOK", line, status)
	}
	if got := out.String(); strings.Contains(got, "[A") || strings.Contains(got, "title") {
		t.Fatalf("escape sequence leaked into echoed input: %q", got)
	}
}

func TestSanitizeTerminalBytesStripsEscapesAndControls(t *testing.T) {
	got := string(sanitizeTerminalBytes([]byte("ok\x1b[2J\x1b]0;title\x07\tstill\x07\n")))
	want := "ok\tstill\n"
	if got != want {
		t.Fatalf("sanitizeTerminalBytes() = %q, want %q", got, want)
	}
}

func TestSanitizingWriterHandlesSplitEscapes(t *testing.T) {
	var out bytes.Buffer
	w := newSanitizingWriter(&out)
	if _, err := w.Write([]byte("ok\x1b[")); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("2Jdone\n")); err != nil {
		t.Fatal(err)
	}
	if got, want := out.String(), "okdone\n"; got != want {
		t.Fatalf("sanitized output = %q, want %q", got, want)
	}
}

func TestCommandTimeoutDuration(t *testing.T) {
	t.Setenv("USE_TOOL_COMMAND_TIMEOUT", "")
	if got := commandTimeoutDuration(); got != defaultCommandTimeout {
		t.Fatalf("default timeout = %s, want %s", got, defaultCommandTimeout)
	}

	t.Setenv("USE_TOOL_COMMAND_TIMEOUT", "30s")
	if got := commandTimeoutDuration(); got != 30*time.Second {
		t.Fatalf("configured timeout = %s, want 30s", got)
	}

	t.Setenv("USE_TOOL_COMMAND_TIMEOUT", "0")
	if got := commandTimeoutDuration(); got != 0 {
		t.Fatalf("disabled timeout = %s, want 0", got)
	}

	t.Setenv("USE_TOOL_COMMAND_TIMEOUT", "-1s")
	if got := commandTimeoutDuration(); got != defaultCommandTimeout {
		t.Fatalf("invalid timeout = %s, want %s", got, defaultCommandTimeout)
	}
}

func TestDangerousCommandReason(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"rm -rf /tmp/use-tool-test", true},
		{"sudo -n rm -rf /etc", true},
		{"dd if=/dev/zero of=/dev/sda bs=1M", true},
		{"mkfs.ext4 /dev/sdb1", true},
		{"systemctl reboot", true},
		{"echo ok; rm -rf /tmp/use-tool-test", true},
		{"printf ok | sudo -n rm -rf /etc", true},
		{"echo 'rm -rf /'", false},
		{"rm build.log", false},
		{"cat /proc/meminfo", false},
	}
	for _, tc := range cases {
		_, got := dangerousCommandReason(tc.cmd)
		if got != tc.want {
			t.Fatalf("dangerousCommandReason(%q) ok = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}

func TestAppendCapturedWarnsBeforeHistoryCap(t *testing.T) {
	s := &Session{}
	for i := 0; i < maxCapturedItems-maxCapturedWarningRemaining-1; i++ {
		s.Captured = append(s.Captured, CapturedCommand{Cmd: "echo old"})
	}

	stderr := captureStderr(func() {
		s.appendCaptured(CapturedCommand{Cmd: "echo new"})
	})
	if !strings.Contains(stderr, "report evidence warning") {
		t.Fatalf("expected report evidence warning, got %q", stderr)
	}
	if !strings.Contains(stderr, "report findings are derived from this history") {
		t.Fatalf("expected report findings context, got %q", stderr)
	}
	if len(s.Captured) != maxCapturedItems-maxCapturedWarningRemaining {
		t.Fatalf("captured count = %d, want %d", len(s.Captured), maxCapturedItems-maxCapturedWarningRemaining)
	}

	stderr = captureStderr(func() {
		s.appendCaptured(CapturedCommand{Cmd: "echo another"})
	})
	if strings.Contains(stderr, "report evidence warning") {
		t.Fatalf("report evidence warning repeated: %q", stderr)
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

func TestRunAndCaptureTreatsGrepNoMatchesAsEmptySuccess(t *testing.T) {
	s := &Session{}
	var c CapturedCommand
	stderr := captureStderr(func() {
		c = s.runAndCapture("printf 'healthy\\n' | grep -iE 'killed process|out of memory|oom-killer'")
	})

	if c.Failed {
		t.Fatalf("grep no-match should be a successful empty diagnostic result, got %+v", c)
	}
	if c.ExitCode != 1 {
		t.Fatalf("exit code = %d, want original grep status 1", c.ExitCode)
	}
	if strings.TrimSpace(c.Output) != "" {
		t.Fatalf("output = %q, want empty", c.Output)
	}
	if len(s.Captured) != 1 {
		t.Fatalf("captured count = %d, want 1", len(s.Captured))
	}
	if !strings.Contains(stderr, "[no matching lines]") {
		t.Fatalf("expected no-match hint in stderr, got:\n%s", stderr)
	}
	if strings.Contains(stderr, "command exited with status 1") {
		t.Fatalf("stderr still reports no-match grep as command failure:\n%s", stderr)
	}
}

func TestRunAndCaptureKeepsRealGrepErrorsFailed(t *testing.T) {
	s := &Session{}
	var c CapturedCommand
	stderr := captureStderr(func() {
		c = s.runAndCapture("grep -E '['")
	})

	if !c.Failed {
		t.Fatalf("invalid grep syntax should fail, got %+v", c)
	}
	if len(s.Captured) != 0 {
		t.Fatalf("captured failed grep command: %+v", s.Captured)
	}
	if !strings.Contains(stderr, "[command exited with status 2]") {
		t.Fatalf("expected real grep error status in stderr, got:\n%s", stderr)
	}
}

func TestRunAndCaptureRejectsGrepMissingPatternBeforeRunning(t *testing.T) {
	s := &Session{}
	var c CapturedCommand
	stderr := captureStderr(func() {
		c = s.runAndCapture("printf 'oom\\n' | grep -ie")
	})

	if !c.Failed {
		t.Fatalf("grep with missing -e pattern should fail preflight, got %+v", c)
	}
	if c.ExitCode != -1 {
		t.Fatalf("exit code = %d, want -1", c.ExitCode)
	}
	if len(s.Captured) != 0 {
		t.Fatalf("captured failed grep command: %+v", s.Captured)
	}
	if !strings.Contains(stderr, "[command not run: grep needs a search pattern") {
		t.Fatalf("expected preflight failure message in stderr, got:\n%s", stderr)
	}
	if strings.Contains(stderr, "grep: option requires an argument") {
		t.Fatalf("grep actually ran instead of being caught by preflight:\n%s", stderr)
	}
}

func TestCommandPreflightRejectsBareGrepFilters(t *testing.T) {
	for _, cmd := range []string{
		"grep",
		"grep -i",
		"dmesg -T | grep -iE",
		"sudo dmesg -T | grep -ie",
		"journalctl -k -b --no-pager | grep --regexp",
	} {
		if reason, ok := commandPreflightFailure(cmd); !ok || !strings.Contains(reason, "grep needs a search pattern") {
			t.Fatalf("commandPreflightFailure(%q) = %q, %v; want grep pattern failure", cmd, reason, ok)
		}
	}
}

func TestCommandPreflightAllowsGrepPatterns(t *testing.T) {
	for _, cmd := range []string{
		"grep oom",
		"grep -iE 'killed process|out of memory|oom-killer'",
		"dmesg -T | grep -ie 'out of memory'",
		"journalctl -k -b --no-pager | grep --regexp='oom-killer'",
		"grep -f patterns.txt /var/log/syslog",
		"grep -- -leading-dash file.txt",
	} {
		if reason, ok := commandPreflightFailure(cmd); ok {
			t.Fatalf("commandPreflightFailure(%q) = %q, true; want allowed", cmd, reason)
		}
	}
}

func TestLastPipelineCommandBaseIgnoresQuotedRegexPipes(t *testing.T) {
	cmd := "journalctl -k -b --no-pager | grep -iE 'killed process|out of memory|oom-killer'"
	if got := lastPipelineCommandBase(cmd); got != "grep" {
		t.Fatalf("lastPipelineCommandBase() = %q, want grep", got)
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
	if !strings.Contains(stderr, "try again with sudo") {
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

func TestChooseGuideQuestionsUsesRequestedRandomSubset(t *testing.T) {
	oldRand := appRand
	defer func() { appRand = oldRand }()
	appRand = rand.New(rand.NewSource(1))

	questions := []Question{
		{Stem: "q1"},
		{Stem: "q2"},
		{Stem: "q3"},
		{Stem: "q4"},
		{Stem: "q5"},
	}

	subsets := map[string]bool{}
	for i := 0; i < 20; i++ {
		chosen := chooseGuideQuestions(questions, 3)
		if len(chosen) != 3 {
			t.Fatalf("iteration %d: expected 3 questions, got %d", i, len(chosen))
		}
		seen := map[string]bool{}
		var stems []string
		for _, q := range chosen {
			if seen[q.Stem] {
				t.Fatalf("iteration %d: duplicate question %q in %#v", i, q.Stem, chosen)
			}
			seen[q.Stem] = true
			stems = append(stems, q.Stem)
		}
		sort.Strings(stems)
		subsets[strings.Join(stems, ",")] = true
	}
	if len(subsets) < 2 {
		t.Fatalf("guide question selection did not vary; saw subsets %v", subsets)
	}
	if len(questions) != 5 || questions[0].Stem != "q1" {
		t.Fatalf("chooseGuideQuestions mutated original questions: %#v", questions)
	}
}

func TestWideColumnGuideStepsAskThreeHeaderQuestions(t *testing.T) {
	cases := []struct {
		name     string
		step     GuideStep
		captured CapturedCommand
	}{
		{
			name: "cpu mpstat",
			step: mustFindGuideStep(t, cpuSteps(SystemInfo{HasMpstat: true}), "per-cpu"),
			captured: CapturedCommand{
				Cmd:    "mpstat -P ALL 1 1",
				Output: sampleMpstat,
			},
		},
		{
			name: "cpu vmstat",
			step: mustFindGuideStep(t, cpuSteps(SystemInfo{}), "runqueue"),
			captured: CapturedCommand{
				Cmd:    "vmstat 1 3",
				Output: sampleVmstat,
			},
		},
		{
			name: "memory meminfo",
			step: mustFindGuideStep(t, memorySteps(SystemInfo{}), "baseline"),
			captured: CapturedCommand{
				Cmd:    "cat /proc/meminfo",
				Output: sampleMeminfo,
			},
		},
		{
			name: "memory PSI",
			step: mustFindGuideStep(t, memorySteps(SystemInfo{HasMemoryPSI: true}), "pressure"),
			captured: CapturedCommand{
				Cmd:    "cat /proc/pressure/memory",
				Output: samplePSIMemory,
			},
		},
		{
			name: "memory vmstat",
			step: mustFindGuideStep(t, memorySteps(SystemInfo{}), "swap-activity"),
			captured: CapturedCommand{
				Cmd:    "vmstat 1 3",
				Output: sampleVmstat,
			},
		},
		{
			name: "memory ps",
			step: mustFindGuideStep(t, memorySteps(SystemInfo{}), "top-consumers"),
			captured: CapturedCommand{
				Cmd: "ps -eo pid,rss,comm --sort=-rss | head -10",
				Output: `    PID   RSS COMMAND
   1234 204800 postgres
   5678 102400 worker
`,
			},
		},
		{
			name: "disk lsblk",
			step: mustFindGuideStep(t, diskSteps(SystemInfo{}), "devices"),
			captured: CapturedCommand{
				Cmd:    "lsblk",
				Output: sampleLsblk,
			},
		},
		{
			name: "disk iostat",
			step: mustFindGuideStep(t, diskSteps(SystemInfo{}), "throughput"),
			captured: CapturedCommand{
				Cmd:    "iostat -xz 1 1",
				Output: sampleIostatModern,
			},
		},
		{
			name: "disk PSI",
			step: mustFindGuideStep(t, diskSteps(SystemInfo{HasIOPSI: true}), "pressure"),
			captured: CapturedCommand{
				Cmd:    "cat /proc/pressure/io",
				Output: samplePSIIO,
			},
		},
		{
			name: "disk pidstat",
			step: mustFindGuideStep(t, diskSteps(SystemInfo{}), "attribution"),
			captured: CapturedCommand{
				Cmd: "pidstat -d 1 1",
				Output: `Linux 6.1.0  10/05/2024
14:00:00       UID       PID   kB_rd/s   kB_wr/s kB_ccwr/s iodelay  Command
14:00:01      1000      1234     12.50    400.00      0.00       2  postgres
`,
			},
		},
		{
			name: "network ip link",
			step: mustFindGuideStep(t, networkSteps(SystemInfo{}), "interfaces"),
			captured: CapturedCommand{
				Cmd:    "ip -s link",
				Output: sampleIpLink,
			},
		},
		{
			name: "network sar DEV",
			step: mustFindGuideStep(t, networkSteps(SystemInfo{}), "throughput"),
			captured: CapturedCommand{
				Cmd:    "sar -n DEV 1 2",
				Output: sampleSarDev,
			},
		},
		{
			name: "network sar EDEV",
			step: mustFindGuideStep(t, networkSteps(SystemInfo{}), "drops"),
			captured: CapturedCommand{
				Cmd:    "sar -n EDEV 1 2",
				Output: sampleSarEdev,
			},
		},
		{
			name: "network sockets",
			step: mustFindGuideStep(t, networkSteps(SystemInfo{}), "sockets"),
			captured: CapturedCommand{
				Cmd:    "ss -s",
				Output: sampleSsSummary,
			},
		},
	}

	for _, tc := range cases {
		if tc.step.QuestionCount != 3 {
			t.Fatalf("%s: QuestionCount = %d, want 3", tc.name, tc.step.QuestionCount)
		}
		questions := guideQuestions(SystemInfo{}, tc.step, tc.captured)
		if len(questions) < 3 {
			t.Fatalf("%s: expected at least 3 available header questions, got %d", tc.name, len(questions))
		}
		chosen := chooseGuideQuestions(questions, tc.step.QuestionCount)
		if len(chosen) != 3 {
			t.Fatalf("%s: selected %d questions, want 3", tc.name, len(chosen))
		}
		for _, q := range chosen {
			if !strings.Contains(q.Stem, "represent") {
				t.Fatalf("%s: selected non-header question %q", tc.name, q.Stem)
			}
		}
	}
}

func TestGuideQuestionAnswersAvoidPromptTermGiveaways(t *testing.T) {
	pidstatDOutput := `Linux 6.1.0  10/05/2024
14:00:00       UID       PID   kB_rd/s   kB_wr/s kB_ccwr/s iodelay  Command
14:00:01      1000      1234     12.50    400.00      0.00       2  postgres
`
	psRSSOutput := `    PID   RSS COMMAND
   1234 204800 postgres
   5678 102400 worker
`

	cases := []struct {
		name         string
		questions    []Question
		stemFragment string
		forbidden    []string
	}{
		{
			name:         "cpu vmstat user",
			questions:    vmstatColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "vmstat 1 3", Output: sampleVmstat}),
			stemFragment: "`us`",
			forbidden:    []string{"user"},
		},
		{
			name:         "cpu mpstat user",
			questions:    mpstatColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "mpstat -P ALL 1 3", Output: sampleMpstat}),
			stemFragment: "`%usr`",
			forbidden:    []string{"user"},
		},
		{
			name:         "cpu sar user",
			questions:    sarUColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "sar -u 1 3", Output: sampleSarU}),
			stemFragment: "`%user`",
			forbidden:    []string{"user"},
		},
		{
			name:         "cpu sar nice",
			questions:    sarUColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "sar -u 1 3", Output: sampleSarU}),
			stemFragment: "`%nice`",
			forbidden:    []string{"nice"},
		},
		{
			name:         "cpu mpstat iowait",
			questions:    mpstatColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "mpstat -P ALL 1 3", Output: sampleMpstat}),
			stemFragment: "`%iowait`",
			forbidden:    []string{"wait"},
		},
		{
			name:         "cpu sar iowait",
			questions:    sarUColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "sar -u 1 3", Output: sampleSarU}),
			stemFragment: "`%iowait`",
			forbidden:    []string{"wait"},
		},
		{
			name:         "cpu mpstat soft",
			questions:    mpstatColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "mpstat -P ALL 1 3", Output: sampleMpstat}),
			stemFragment: "`%soft`",
			forbidden:    []string{"software", "soft"},
		},
		{
			name:         "cpu sar steal",
			questions:    sarUColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "sar -u 1 3", Output: sampleSarU}),
			stemFragment: "`%steal`",
			forbidden:    []string{"steal", "stolen"},
		},
		{
			name:         "cpu mpstat steal",
			questions:    mpstatColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "mpstat -P ALL 1 3", Output: sampleMpstat}),
			stemFragment: "`%steal`",
			forbidden:    []string{"steal", "stolen"},
		},
		{
			name:         "cpu sar idle",
			questions:    sarUColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "sar -u 1 3", Output: sampleSarU}),
			stemFragment: "`%idle`",
			forbidden:    []string{"idle"},
		},
		{
			name:         "memory memavailable",
			questions:    meminfoColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "cat /proc/meminfo", Output: sampleMeminfo}),
			stemFragment: "`MemAvailable`",
			forbidden:    []string{"available"},
		},
		{
			name:         "memory meminfo buffers",
			questions:    meminfoColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "cat /proc/meminfo", Output: sampleMeminfo}),
			stemFragment: "`Buffers`",
			forbidden:    []string{"buffer"},
		},
		{
			name:         "memory meminfo cached",
			questions:    meminfoColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "cat /proc/meminfo", Output: sampleMeminfo}),
			stemFragment: "`Cached`",
			forbidden:    []string{"cache"},
		},
		{
			name:         "memory free shared",
			questions:    freeColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "free -h", Output: sampleFreeH}),
			stemFragment: "`shared`",
			forbidden:    []string{"shared"},
		},
		{
			name:         "memory free buff cache",
			questions:    freeColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "free -h", Output: sampleFreeH}),
			stemFragment: "`buff/cache`",
			forbidden:    []string{"buffer", "cache"},
		},
		{
			name:         "memory sar swap-in",
			questions:    sarWColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "sar -W 1 5", Output: sampleSarW}),
			stemFragment: "`pswpin/s`",
			forbidden:    []string{"swapped in"},
		},
		{
			name:         "memory sar swap-out",
			questions:    sarWColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "sar -W 1 5", Output: sampleSarW}),
			stemFragment: "`pswpout/s`",
			forbidden:    []string{"swapped out"},
		},
		{
			name:         "memory top virt",
			questions:    topMemColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "top -bn1 -o %MEM", Output: sampleTopMem}),
			stemFragment: "`VIRT`",
			forbidden:    []string{"virtual"},
		},
		{
			name:         "memory top res",
			questions:    topMemColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "top -bn1 -o %MEM", Output: sampleTopMem}),
			stemFragment: "`RES`",
			forbidden:    []string{"resident"},
		},
		{
			name:         "memory top shr",
			questions:    topMemColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "top -bn1 -o %MEM", Output: sampleTopMem}),
			stemFragment: "`SHR`",
			forbidden:    []string{"shared"},
		},
		{
			name:         "memory ps rss",
			questions:    psRssColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "ps -eo pid,rss,comm", Output: psRSSOutput}),
			stemFragment: "`RSS`",
			forbidden:    []string{"resident"},
		},
		{
			name:         "disk partitions blocks",
			questions:    procPartitionsColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "cat /proc/partitions", Output: sampleProcPartitions}),
			stemFragment: "`#blocks`",
			forbidden:    []string{"blocks"},
		},
		{
			name:         "disk lsblk size",
			questions:    lsblkColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "lsblk", Output: sampleLsblk}),
			stemFragment: "`SIZE`",
			forbidden:    []string{"size"},
		},
		{
			name:         "disk lsblk type",
			questions:    lsblkColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "lsblk", Output: sampleLsblk}),
			stemFragment: "`TYPE`",
			forbidden:    []string{"type"},
		},
		{
			name:         "disk pidstat iodelay",
			questions:    pidstatDColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "pidstat -d 1 1", Output: pidstatDOutput}),
			stemFragment: "`iodelay`",
			forbidden:    []string{"delay"},
		},
		{
			name:         "disk pidstat command",
			questions:    pidstatDColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "pidstat -d 1 1", Output: pidstatDOutput}),
			stemFragment: "`Command`",
			forbidden:    []string{"command"},
		},
		{
			name:         "network sar dev iface",
			questions:    sarDevColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "sar -n DEV 1 2", Output: sampleSarDev}),
			stemFragment: "`IFACE`",
			forbidden:    []string{"interface"},
		},
		{
			name:         "network sar dev ifutil",
			questions:    sarDevColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "sar -n DEV 1 2", Output: sampleSarDev}),
			stemFragment: "`%ifutil`",
			forbidden:    []string{"interface", "utilization", "util"},
		},
		{
			name:         "network ss estab",
			questions:    ssSummaryColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "ss -s", Output: sampleSsSummary}),
			stemFragment: "`estab`",
			forbidden:    []string{"established"},
		},
		{
			name:         "network ss closed",
			questions:    ssSummaryColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "ss -s", Output: sampleSsSummary}),
			stemFragment: "`closed`",
			forbidden:    []string{"closed"},
		},
		{
			name:         "network ss timewait",
			questions:    ssSummaryColumnQuestions(SystemInfo{}, CapturedCommand{Cmd: "ss -s", Output: sampleSsSummary}),
			stemFragment: "`timewait`",
			forbidden:    []string{"time_wait", "time wait"},
		},
	}

	for _, tc := range cases {
		q, ok := findQuestionByStemFragment(tc.questions, tc.stemFragment)
		if !ok {
			t.Fatalf("%s: no question contained %q; got %v", tc.name, tc.stemFragment, stems(tc.questions))
		}
		for _, answer := range questionAnswerTexts(q) {
			normalized := strings.ToLower(answer)
			for _, term := range tc.forbidden {
				if strings.Contains(normalized, term) {
					t.Fatalf("%s: answer option %q still echoes prompt term %q", tc.name, answer, term)
				}
			}
		}
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
		{"memory", memorySteps(SystemInfo{HasMemoryPSI: true})},
		{"disk", diskSteps(SystemInfo{HasIOPSI: true})},
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
		withJournal, ok := findGuideStep(tc.stepsFn(SystemInfo{HasPSI: true, HasMemoryPSI: true, HasIOPSI: true, HasJournalctl: true}), "errors")
		if !ok {
			t.Fatalf("%s: expected errors step", tc.resource)
		}
		if len(withJournal.Alternatives) != 1 || !strings.Contains(withJournal.Alternatives[0], "journalctl -k -b") {
			t.Fatalf("%s: expected one journalctl alternative, got %#v", tc.resource, withJournal.Alternatives)
		}
		if strings.Contains(withJournal.Intro, "journalctl") {
			t.Fatalf("%s: intro should not unconditionally mention journalctl: %s", tc.resource, withJournal.Intro)
		}

		withoutJournal, ok := findGuideStep(tc.stepsFn(SystemInfo{HasPSI: true, HasMemoryPSI: true, HasIOPSI: true}), "errors")
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
		step, ok := findGuideStep(inv.StepsFn(SystemInfo{HasPSI: true, HasMemoryPSI: true, HasIOPSI: true}), "errors")
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

func findQuestionByStemFragment(qs []Question, fragment string) (Question, bool) {
	for _, q := range qs {
		if strings.Contains(q.Stem, fragment) {
			return q, true
		}
	}
	return Question{}, false
}

func questionAnswerTexts(q Question) []string {
	answers := []string{q.Correct}
	answers = append(answers, q.Distractors...)
	return answers
}

func mustFindGuideStep(t *testing.T, steps []GuideStep, name string) GuideStep {
	t.Helper()
	step, ok := findGuideStep(steps, name)
	if !ok {
		t.Fatalf("expected guide step %q", name)
	}
	return step
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
