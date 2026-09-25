package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"
	"unsafe"
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

func TestBareCommandUsesSubcommandSelector(t *testing.T) {
	oldArgs := os.Args
	oldSelector := menuSelector
	defer func() {
		os.Args = oldArgs
		menuSelector = oldSelector
	}()

	os.Args = []string{"use-tool"}
	menuSelector = func(spec menuSpec) (string, error) {
		if spec.Title != "use-tool - choose a command" {
			t.Fatalf("selector title = %q", spec.Title)
		}
		if !optionValuesContain(spec.Options, "guide", "practice", "commands", "list", "version", "help") {
			t.Fatalf("selector options = %#v", spec.Options)
		}
		return "version", nil
	}

	out := captureStdout(main)
	if !strings.Contains(out, "use-tool ") {
		t.Fatalf("version output missing app name: %q", out)
	}
}

func TestResourceMenusMatchSubcommand(t *testing.T) {
	practice := resourceMenuOptions("practice")
	if !optionValuesContain(practice, "cpu", "disk", "memory", "network", "system") {
		t.Fatalf("practice resources = %#v", practice)
	}

	guide := resourceMenuOptions("guide")
	if !optionValuesContain(guide, "cpu", "disk", "memory", "network") {
		t.Fatalf("guide resources = %#v", guide)
	}
	if optionValuesContain(guide, "system") {
		t.Fatalf("guide resources should not include practice-only system target: %#v", guide)
	}
}

func TestChooseResourceUsesSelector(t *testing.T) {
	oldSelector := menuSelector
	defer func() { menuSelector = oldSelector }()

	menuSelector = func(spec menuSpec) (string, error) {
		if spec.Title != "use-tool practice - choose a resource" {
			t.Fatalf("selector title = %q", spec.Title)
		}
		if spec.Fallback != "cpu" {
			t.Fatalf("selector fallback = %q, want cpu", spec.Fallback)
		}
		return "memory", nil
	}

	got, err := chooseResourceForCommand("practice")
	if err != nil {
		t.Fatal(err)
	}
	if got != "memory" {
		t.Fatalf("chooseResourceForCommand() = %q, want memory", got)
	}
}

func TestCommandsWithoutResourceUsesResourceSelector(t *testing.T) {
	oldSelector := menuSelector
	defer func() { menuSelector = oldSelector }()

	menuSelector = func(spec menuSpec) (string, error) {
		if spec.Title != "use-tool commands - choose a resource" {
			t.Fatalf("selector title = %q", spec.Title)
		}
		return "network", nil
	}

	out := captureStdout(func() {
		cmdCommands(nil)
	})
	if !strings.Contains(out, "Network — Utilization, Saturation, Errors — command reference") {
		t.Fatalf("commands output did not use selected resource:\n%s", out)
	}
}

func TestRenderMenuSelectorIncludesOptionsAndHelp(t *testing.T) {
	lines := 0
	got := captureStdout(func() {
		lines = renderMenuSelector("choose", []menuOption{
			{Label: "guide", Summary: "Guided walkthrough"},
			{Label: "practice", Summary: "Free-form investigation"},
		}, 1, true)
	})
	for _, want := range []string{
		"choose",
		"  1. guide",
		"> 2. practice",
		"Up/k, Down/j",
		"Enter",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered selector missing %q:\n%s", want, got)
		}
	}
	if lines == 0 {
		t.Fatal("renderMenuSelector reported no lines")
	}
}

func TestRedrawMenuSelectorClearsScreenOnEveryRender(t *testing.T) {
	options := []menuOption{
		{Label: "guide", Summary: "Guided walkthrough"},
		{Label: "practice", Summary: "Free-form investigation"},
	}

	got := captureStdout(func() {
		redrawMenuSelector("choose", options, 0, false)
		redrawMenuSelector("choose", options, 1, false)
	})

	if count := strings.Count(got, "\x1b[H\x1b[2J"); count != 2 {
		t.Fatalf("clear-screen count = %d, want 2 in %q", count, got)
	}
	if !strings.Contains(got, "> 1. guide") || !strings.Contains(got, "> 2. practice") {
		t.Fatalf("redraws did not render both selections:\n%s", got)
	}
}

func optionValuesContain(options []menuOption, values ...string) bool {
	have := map[string]bool{}
	for _, option := range options {
		have[option.Value] = true
	}
	for _, value := range values {
		if !have[value] {
			return false
		}
	}
	return true
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

func TestRunCommandTerminalJobControl(t *testing.T) {
	if mode := os.Getenv("USE_TOOL_TEST_PTY_MODE"); mode != "" {
		t.Setenv("USE_TOOL_COMMAND_TIMEOUT", "5s")
		var captured CapturedCommand
		switch mode {
		case "cached":
			captured = runCommand(`printf 'child-cached\n'`)
		case "prompt":
			captured = runCommand(`printf 'child-ready\n'; IFS= read -r value; printf 'child-read:%s\n' "$value"`)
		case "interrupt":
			captured = runCommand(`printf 'child-ready\n'; IFS= read -r value`)
		default:
			t.Fatalf("unknown PTY helper mode %q", mode)
		}
		fmt.Printf("helper-finished failed=%t\n", captured.Failed)
		return
	}

	t.Run("command without prompt", func(t *testing.T) {
		output := runPTYTestProcess(t, "cached", nil)
		for _, want := range []string{"child-cached", "helper-finished failed=false"} {
			if !strings.Contains(output, want) {
				t.Fatalf("PTY output missing %q:\n%s", want, output)
			}
		}
	})

	t.Run("command reads terminal", func(t *testing.T) {
		output := runPTYTestProcess(t, "prompt", []byte("hello\n"))
		for _, want := range []string{"child-ready", "child-read:hello", "helper-finished failed=false"} {
			if !strings.Contains(output, want) {
				t.Fatalf("PTY output missing %q:\n%s", want, output)
			}
		}
	})

	t.Run("ctrl-c returns control", func(t *testing.T) {
		output := runPTYTestProcess(t, "interrupt", []byte{3})
		for _, want := range []string{"child-ready", "helper-finished failed=true"} {
			if !strings.Contains(output, want) {
				t.Fatalf("PTY output missing %q:\n%s", want, output)
			}
		}
	})
}

type ptyReadResult struct {
	output string
	err    error
}

func runPTYTestProcess(t *testing.T, mode string, inputAfterReady []byte) string {
	t.Helper()
	master, slave := openTestPTY(t)
	defer master.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestRunCommandTerminalJobControl$")
	cmd.Env = append(os.Environ(), "USE_TOOL_TEST_PTY_MODE="+mode)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		slave.Close()
		t.Fatalf("start PTY helper: %v", err)
	}
	if err := slave.Close(); err != nil {
		t.Fatalf("close parent PTY slave: %v", err)
	}

	readDone := make(chan ptyReadResult, 1)
	go func() {
		var output bytes.Buffer
		buf := make([]byte, 512)
		inputSent := len(inputAfterReady) == 0
		for {
			n, err := master.Read(buf)
			if n > 0 {
				output.Write(buf[:n])
				if !inputSent && strings.Contains(output.String(), "child-ready") {
					_, writeErr := master.Write(inputAfterReady)
					if writeErr != nil {
						readDone <- ptyReadResult{output: output.String(), err: writeErr}
						return
					}
					inputSent = true
				}
			}
			if err != nil {
				if errors.Is(err, syscall.EIO) {
					err = nil
				}
				readDone <- ptyReadResult{output: output.String(), err: err}
				return
			}
		}
	}()

	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()

	var (
		output  ptyReadResult
		waitErr error
		readOK  bool
		waitOK  bool
	)
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()
	for !readOK || !waitOK {
		select {
		case output = <-readDone:
			readOK = true
		case waitErr = <-waitDone:
			waitOK = true
		case <-timer.C:
			_ = master.Close()
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			_ = cmd.Process.Kill()
			t.Fatalf("PTY helper timed out in mode %q", mode)
		}
	}
	if output.err != nil {
		t.Fatalf("read PTY output: %v", output.err)
	}
	if waitErr != nil {
		t.Fatalf("PTY helper failed: %v\n%s", waitErr, output.output)
	}
	return output.output
}

func openTestPTY(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	master, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		t.Fatalf("open PTY master: %v", err)
	}

	var unlock int32
	if err := testIoctl(master.Fd(), syscall.TIOCSPTLCK, unsafe.Pointer(&unlock)); err != nil {
		master.Close()
		t.Fatalf("unlock PTY: %v", err)
	}
	var number uint32
	if err := testIoctl(master.Fd(), syscall.TIOCGPTN, unsafe.Pointer(&number)); err != nil {
		master.Close()
		t.Fatalf("get PTY number: %v", err)
	}

	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", number), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		master.Close()
		t.Fatalf("open PTY slave: %v", err)
	}
	return master, slave
}

func testIoctl(fd uintptr, request uint, value unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, uintptr(request), uintptr(value))
	if errno != 0 {
		return errno
	}
	return nil
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

func TestGuideStepCommandRetriesUnexpectedAcceptAnyCommand(t *testing.T) {
	oldStdin := stdin
	defer func() { stdin = oldStdin }()
	stdin = bufio.NewReader(strings.NewReader("printf wrong\necho ok\n"))

	var captured *CapturedCommand
	out := captureStdout(func() {
		captured = guideStepCommand(&Session{}, GuideStep{AcceptAny: true, Suggested: "echo ok"})
	})
	if captured == nil || captured.Cmd != "echo ok" {
		t.Fatalf("captured = %+v, want expected command after retry", captured)
	}
	if !strings.Contains(out, "That command didn't produce output this step recognizes") {
		t.Fatalf("expected feedback for successful but unrelated command:\n%s", out)
	}
}

func TestGuideStepCommandDoesNotDescribeWrongEmptyCommandAsHealthy(t *testing.T) {
	oldStdin := stdin
	defer func() { stdin = oldStdin }()
	stdin = bufio.NewReader(strings.NewReader("true\nprintf ''\n\n"))

	out := captureStdout(func() {
		captured := guideStepCommand(&Session{}, GuideStep{
			Suggested:          "printf ''",
			AcceptAny:          true,
			EmptyOutputMessage: "No matching errors found.",
		})
		if captured == nil || captured.Cmd != "printf ''" {
			t.Fatalf("captured = %+v, want expected empty command after retry", captured)
		}
	})
	if count := strings.Count(out, "No matching errors found."); count != 1 {
		t.Fatalf("healthy empty-output message appeared %d times, want once:\n%s", count, out)
	}
}

func TestGuideStepCommandSeparatesUnrecognizedFeedbackFromOutput(t *testing.T) {
	oldStdin := stdin
	defer func() { stdin = oldStdin }()

	for _, tc := range []struct {
		name    string
		command string
		output  string
	}{
		{name: "terminated output", command: `printf 'unrecognized output\n'`, output: "unrecognized output"},
		{name: "unterminated output", command: `printf 'unrecognized output'`, output: "unrecognized output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stdin = bufio.NewReader(strings.NewReader(tc.command + "\nskip\n"))
			out := captureStdout(func() {
				guideStepCommand(&Session{}, GuideStep{Suggested: "echo expected"})
			})

			want := tc.output + "\n\n(That command didn't produce output this step recognizes"
			if !strings.Contains(out, want) {
				t.Fatalf("expected a blank line before unrecognized-output feedback:\n%s", out)
			}
			feedback := "(That command didn't produce output this step recognizes — try `echo expected`, or `skip`.)"
			if !strings.Contains(out, feedback+"\n\n[guide] $ ") {
				t.Fatalf("expected a blank line between unrecognized-output feedback and the next prompt:\n%s", out)
			}
		})
	}
}

func TestGuideQuestionsNilSafe(t *testing.T) {
	got := guideQuestions(SystemInfo{}, GuideStep{}, CapturedCommand{Cmd: "lsblk", Output: "NAME TYPE\n"})
	if got != nil {
		t.Fatalf("guideQuestions with nil QuestionsFn = %v, want nil", got)
	}
}

func TestGuideQuestionsRejectsUnexpectedCommandEvenWhenOutputMatches(t *testing.T) {
	step := mustFindGuideStep(t, cpuSteps(SystemInfo{HasMpstat: true}), "per-cpu")
	captured := CapturedCommand{Cmd: "iostat 1 3", Output: sampleIostatModern}
	if raw := step.QuestionsFn(SystemInfo{}, captured); len(raw) == 0 {
		t.Fatal("test fixture no longer overlaps the mpstat output parser")
	}
	if questions := guideQuestions(SystemInfo{}, step, captured); questions != nil {
		t.Fatalf("expected no mpstat questions for iostat, got %v", stems(questions))
	}
}

func TestGuideQuestionsRejectsWrongProcPressureFile(t *testing.T) {
	step := mustFindGuideStep(t, cpuSteps(SystemInfo{HasPSI: true}), "runqueue")
	captured := CapturedCommand{
		Cmd: "cat /proc/pressure/io",
		Output: "some avg10=0.00 avg60=0.06 avg300=0.21 total=2373245647\n" +
			"full avg10=0.00 avg60=0.04 avg300=0.17 total=2038554429\n",
	}
	if raw := procPressureCpuQuestions(SystemInfo{}, captured); len(raw) == 0 {
		t.Fatal("test fixture no longer overlaps the CPU PSI output parser")
	}
	if questions := guideQuestions(SystemInfo{}, step, captured); questions != nil {
		t.Fatalf("expected no CPU questions for /proc/pressure/io, got %v", stems(questions))
	}
}

func TestGuideCommandMatchesMeaningfulArguments(t *testing.T) {
	for _, tc := range []struct {
		name     string
		actual   string
		expected string
		want     bool
	}{
		{name: "exact proc file", actual: "cat /proc/pressure/cpu", expected: "cat /proc/pressure/cpu", want: true},
		{name: "wrong proc file", actual: "cat /proc/pressure/io", expected: "cat /proc/pressure/cpu", want: false},
		{name: "wrapped command", actual: "sudo -n cat /proc/pressure/cpu", expected: "cat /proc/pressure/cpu", want: true},
		{name: "different sample count", actual: "vmstat 2 7", expected: "vmstat 1 3", want: true},
		{name: "matching sar report", actual: "sar -u 2 5", expected: "sar -u 1 3", want: true},
		{name: "different sar report", actual: "sar -d 1 3", expected: "sar -u 1 3", want: false},
		{name: "different ss report", actual: "ss -s", expected: "ss -tin", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := guideCommandMatches(tc.actual, tc.expected); got != tc.want {
				t.Fatalf("guideCommandMatches(%q, %q) = %v, want %v", tc.actual, tc.expected, got, tc.want)
			}
		})
	}
}

func TestGuideQuestionsAcceptsAnExpectedVariantThatWasNotSuggested(t *testing.T) {
	variants := cpuRunqueueVariants(SystemInfo{HasSar: true})
	step := GuideStep{
		Suggested:        "vmstat 1 3",
		ExpectedCommands: stepVariantCommands(variants),
		QuestionsFn:      combineVariantQuestions(variants),
	}
	captured := CapturedCommand{Cmd: "sar -u 1 3", Output: sampleSarU}
	if questions := guideQuestions(SystemInfo{HasSar: true}, step, captured); len(questions) == 0 {
		t.Fatal("expected sar questions when sar is an accepted runqueue variant")
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

func TestChooseGuideQuestionsRandomizesQuestionOrder(t *testing.T) {
	oldRand := appRand
	defer func() { appRand = oldRand }()
	appRand = rand.New(rand.NewSource(1))

	questions := []Question{
		{Stem: "q1"},
		{Stem: "q2"},
		{Stem: "q3"},
		{Stem: "q4"},
	}
	want := map[string]bool{"q1": true, "q2": true, "q3": true, "q4": true}
	orders := map[string]bool{}
	for i := 0; i < 20; i++ {
		chosen := chooseGuideQuestions(questions, len(questions))
		if len(chosen) != len(questions) {
			t.Fatalf("iteration %d: got %d questions, want %d", i, len(chosen), len(questions))
		}
		seen := map[string]bool{}
		order := make([]string, 0, len(chosen))
		for _, q := range chosen {
			if !want[q.Stem] || seen[q.Stem] {
				t.Fatalf("iteration %d: result is not a permutation: %#v", i, chosen)
			}
			seen[q.Stem] = true
			order = append(order, q.Stem)
		}
		orders[strings.Join(order, ",")] = true
	}
	if len(orders) < 2 {
		t.Fatalf("guide question order did not vary; saw %v", orders)
	}
	if len(questions) != 4 || questions[0].Stem != "q1" {
		t.Fatalf("chooseGuideQuestions mutated original questions: %#v", questions)
	}
}

func TestChooseUnseenGuideQuestionsBlocksRepeatedConcepts(t *testing.T) {
	oldRand := appRand
	defer func() { appRand = oldRand }()
	appRand = rand.New(rand.NewSource(1))

	seen := map[string]bool{}
	first := Question{
		Stem:     "What does mpstat %steal mean?",
		Correct:  "mpstat wording",
		Concepts: []string{"cpu-steal-time"},
	}
	if got := chooseUnseenGuideQuestions([]Question{first}, 1, seen); len(got) != 1 {
		t.Fatalf("first selection returned %d questions, want 1", len(got))
	}

	later := []Question{
		{Stem: "What does sar %steal mean?", Correct: "different wording", Concepts: []string{"cpu-steal-time"}},
		{Stem: "What does sar %nice mean?", Correct: "nice time", Concepts: []string{"cpu-nice-time"}},
	}
	got := chooseUnseenGuideQuestions(later, 2, seen)
	if len(got) != 1 || got[0].Stem != later[1].Stem {
		t.Fatalf("later selection = %#v, want only the unseen nice-time question", got)
	}
}

func TestChooseGuideQuestionsAvoidsSimilarQuestionsWithinOneStep(t *testing.T) {
	questions := []Question{
		{Stem: "read await", Correct: "read latency", Concepts: []string{"disk-request-latency"}},
		{Stem: "write await", Correct: "write latency", Concepts: []string{"disk-request-latency"}},
		{Stem: "queue depth", Correct: "outstanding requests", Concepts: []string{"disk-queue-depth"}},
	}
	got := chooseGuideQuestions(questions, len(questions))
	if len(got) != 2 {
		t.Fatalf("selected %d questions, want one latency question plus queue depth: %#v", len(got), got)
	}
}

func TestRunGuideStepCarriesQuestionConceptsAcrossSteps(t *testing.T) {
	oldStdin := stdin
	oldRaw := rawInputEnabled
	defer func() {
		stdin = oldStdin
		rawInputEnabled = oldRaw
	}()
	stdin = bufio.NewReader(strings.NewReader("echo first\n1\necho second\n"))
	rawInputEnabled = func() bool { return false }

	questionFor := func(stem string) func(SystemInfo, CapturedCommand) []Question {
		return func(SystemInfo, CapturedCommand) []Question {
			return []Question{{
				Stem:        stem,
				Correct:     "The shared idea",
				Distractors: []string{"Something else"},
				Concepts:    []string{"shared-concept"},
			}}
		}
	}
	s := &Session{}
	first := GuideStep{Suggested: "echo first", QuestionsFn: questionFor("first check")}
	second := GuideStep{Suggested: "echo second", QuestionsFn: questionFor("second check")}

	var firstAnswered, secondAnswered int
	out := captureStdout(func() {
		_, firstAnswered, _ = runGuideStep(s, first, true)
		_, secondAnswered, _ = runGuideStep(s, second, true)
	})
	if firstAnswered != 1 || secondAnswered != 0 {
		t.Fatalf("answered counts = %d then %d, want 1 then 0\n%s", firstAnswered, secondAnswered, out)
	}
	if checks := strings.Count(out, "--- Check ---"); checks != 1 {
		t.Fatalf("printed %d checks, want 1\n%s", checks, out)
	}
}

func TestGuideQuestionConceptAuditAcrossResources(t *testing.T) {
	pidstatDOutput := `Linux 6.1.0  10/05/2024
14:00:00       UID       PID   kB_rd/s   kB_wr/s kB_ccwr/s iodelay  Command
14:00:01      1000      1234     12.50    400.00      0.00       2  postgres
`

	tests := []struct {
		name          string
		first, second Question
	}{
		{
			name:   "cpu steal across mpstat and sar",
			first:  mustFindQuestion(t, mpstatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleMpstat}), "`%steal`"),
			second: mustFindQuestion(t, sarUColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarU}), "`%steal`"),
		},
		{
			name:   "cpu user time across mpstat and sar",
			first:  mustFindQuestion(t, mpstatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleMpstat}), "`%usr`"),
			second: mustFindQuestion(t, sarUColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarU}), "`%user`"),
		},
		{
			name:   "cpu system time across mpstat and sar",
			first:  mustFindQuestion(t, mpstatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleMpstat}), "`%sys`"),
			second: mustFindQuestion(t, sarUColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarU}), "`%system`"),
		},
		{
			name:   "cpu iowait across mpstat and sar",
			first:  mustFindQuestion(t, mpstatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleMpstat}), "`%iowait`"),
			second: mustFindQuestion(t, sarUColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarU}), "`%iowait`"),
		},
		{
			name:   "cpu idle across mpstat and sar",
			first:  mustFindQuestion(t, mpstatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleMpstat}), "`%idle`"),
			second: mustFindQuestion(t, sarUColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarU}), "`%idle`"),
		},
		{
			name:   "cpu runnable entities across loadavg and vmstat",
			first:  mustFindQuestion(t, procLoadavgQuestions(SystemInfo{}, CapturedCommand{Output: "0.42 0.31 0.28 1/234 5678\n"}), "`1/234`"),
			second: mustFindQuestion(t, vmstatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleVmstat}), "`r`"),
		},
		{
			name:   "memory free across meminfo and vmstat",
			first:  mustFindQuestion(t, meminfoColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleMeminfo}), "`MemFree`"),
			second: mustFindQuestion(t, vmstatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleVmstat}), "`free`"),
		},
		{
			name:   "memory buffers across meminfo and vmstat",
			first:  mustFindQuestion(t, meminfoColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleMeminfo}), "`Buffers`"),
			second: mustFindQuestion(t, vmstatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleVmstat}), "`buff`"),
		},
		{
			name:   "memory swap-in across vmstat and sar",
			first:  mustFindQuestion(t, vmstatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleVmstat}), "`si`"),
			second: mustFindQuestion(t, sarWColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarW}), "`pswpin/s`"),
		},
		{
			name:   "disk device identity across partitions and sar",
			first:  mustFindQuestion(t, procPartitionsColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleProcPartitions}), "`name`"),
			second: mustFindQuestion(t, sarDColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarD}), "`DEV`"),
		},
		{
			name:   "disk read rate across sar and pidstat",
			first:  mustFindQuestion(t, sarDColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarD}), "`rkB/s`"),
			second: mustFindQuestion(t, pidstatDColumnQuestions(SystemInfo{}, CapturedCommand{Output: pidstatDOutput}), "`kB_rd/s`"),
		},
		{
			name:   "disk read and write await in one iostat report",
			first:  mustFindQuestion(t, iostatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleIostatModern}), "`r_await`"),
			second: mustFindQuestion(t, iostatColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleIostatModern}), "`w_await`"),
		},
		{
			name:   "PSI average windows in one pressure report",
			first:  mustFindQuestion(t, psiMemoryColumnQuestions(SystemInfo{}, CapturedCommand{Output: samplePSIMemory}), "`avg10`"),
			second: mustFindQuestion(t, psiMemoryColumnQuestions(SystemInfo{}, CapturedCommand{Output: samplePSIMemory}), "`avg60`"),
		},
		{
			name:   "network receive volume across counters and rate",
			first:  mustFindQuestion(t, ipLinkColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleIpLink}), "RX counters, what does `bytes`"),
			second: mustFindQuestion(t, sarDevColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarDev}), "`rxkB/s`"),
		},
		{
			name:   "network interface identity across sar reports",
			first:  mustFindQuestion(t, sarDevColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarDev}), "`IFACE`"),
			second: mustFindQuestion(t, sarEdevColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarEdev}), "`IFACE`"),
		},
		{
			name:   "network receive drops across counters and rate",
			first:  mustFindQuestion(t, ipLinkColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleIpLink}), "RX counters, what does `dropped`"),
			second: mustFindQuestion(t, sarEdevColumnQuestions(SystemInfo{}, CapturedCommand{Output: sampleSarEdev}), "`rxdrop/s`"),
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seen := map[string]bool{}
			if got := chooseUnseenGuideQuestions([]Question{tc.first}, 1, seen); len(got) != 1 {
				t.Fatalf("first concept was not selectable: %#v", got)
			}
			if got := chooseUnseenGuideQuestions([]Question{tc.second}, 1, seen); len(got) != 0 {
				t.Fatalf("overlapping later question was still selectable: %#v", got)
			}
		})
	}
}

func TestRandomizedQuestionOptionsMovesCorrectAnswer(t *testing.T) {
	oldRand := appRand
	defer func() { appRand = oldRand }()
	appRand = rand.New(rand.NewSource(1))

	q := Question{
		Correct:     "correct",
		Distractors: []string{"wrong-1", "wrong-2", "wrong-3"},
	}
	want := map[string]bool{
		"correct": true, "wrong-1": true, "wrong-2": true, "wrong-3": true,
	}
	correctPositions := map[int]bool{}
	for i := 0; i < 100; i++ {
		options := randomizedQuestionOptions(q)
		if len(options) != len(want) {
			t.Fatalf("iteration %d: got %d options, want %d", i, len(options), len(want))
		}
		seen := map[string]bool{}
		for position, option := range options {
			if !want[option] || seen[option] {
				t.Fatalf("iteration %d: options are not a permutation: %v", i, options)
			}
			seen[option] = true
			if option == q.Correct {
				correctPositions[position] = true
			}
		}
	}
	for position := 0; position < len(want); position++ {
		if !correctPositions[position] {
			t.Errorf("correct answer never appeared at zero-based position %d; saw %v", position, correctPositions)
		}
	}
	if q.Correct != "correct" || strings.Join(q.Distractors, ",") != "wrong-1,wrong-2,wrong-3" {
		t.Fatalf("randomizedQuestionOptions mutated its input: %#v", q)
	}
}

func TestRandomizedQuestionOptionsLimitsCorrectPositionStreak(t *testing.T) {
	oldRand := appRand
	defer func() { appRand = oldRand }()
	appRand = rand.New(rand.NewSource(1))

	q := Question{
		Correct:     "correct",
		Distractors: []string{"wrong-1", "wrong-2", "wrong-3"},
	}
	history := answerPositionHistory{}
	lastPosition, streak := -1, 0
	for i := 0; i < 100; i++ {
		options := randomizedQuestionOptionsWithHistory(q, &history)
		correctPosition := -1
		for position, option := range options {
			if option == q.Correct {
				correctPosition = position
				break
			}
		}
		if correctPosition < 0 {
			t.Fatalf("iteration %d: correct answer missing from %v", i, options)
		}
		if correctPosition == lastPosition {
			streak++
		} else {
			lastPosition = correctPosition
			streak = 1
		}
		if streak > 2 {
			t.Fatalf("iteration %d: correct position %d repeated %d times", i, correctPosition, streak)
		}
		if history.last != correctPosition || history.streak != streak {
			t.Fatalf("iteration %d: history = %+v, want position %d streak %d", i, history, correctPosition, streak)
		}
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

func TestRunGuideStepExplainsNonEmptyUnrecognizedOutput(t *testing.T) {
	oldStdin := stdin
	defer func() { stdin = oldStdin }()
	stdin = bufio.NewReader(strings.NewReader("printf unrelated-warning\n"))

	const message = "No recognized CPU signatures; the warnings may be unrelated."
	out := captureStdout(func() {
		_, _, ok := runGuideStep(&Session{}, GuideStep{
			Suggested:                 "printf unrelated-warning",
			AcceptAny:                 true,
			NoRecognizedOutputMessage: message,
		}, true)
		if !ok {
			t.Fatal("guide step unexpectedly stopped")
		}
	})
	if !strings.Contains(out, message) {
		t.Fatalf("expected no-recognized-output explanation, got:\n%s", out)
	}
}

func TestGuidePausesBeforeFinalSummary(t *testing.T) {
	oldStdin := stdin
	defer func() { stdin = oldStdin }()
	stdin = bufio.NewReader(strings.NewReader("\n"))

	s := &Session{
		Investigation: &Investigation{Name: "cpu"},
		System:        SystemInfo{NumCPU: 2},
	}
	out := captureStdout(func() {
		finishGuide(s, 7, 7)
	})

	pauseAt := strings.Index(out, "Press Enter to continue...")
	summaryAt := strings.Index(out, "--- Snapshot of what you observed ---")
	if pauseAt < 0 || summaryAt < 0 || pauseAt >= summaryAt {
		t.Fatalf("expected pause before final summary:\n%s", out)
	}
	if !strings.Contains(out, "=== Walkthrough complete: 7 / 7 on the inline questions ===") {
		t.Fatalf("expected completion score after final pause:\n%s", out)
	}
}

// orderingGuideStep builds a guide step that captures a trivial command,
// always offers one comprehension check, and carries a teaching note — enough
// to observe the feedback / teaching-note / pause ordering.
func orderingGuideStep(teaching string) GuideStep {
	return GuideStep{
		Name:      "ordering",
		Suggested: "echo USE",
		QuestionsFn: func(SystemInfo, CapturedCommand) []Question {
			return []Question{{
				Stem:        "Which letter does USE stand for first?",
				Correct:     "Utilization",
				Distractors: []string{"Saturation"},
			}}
		},
		Teaching: teaching,
	}
}

func TestGuideStepShowsTeachingNoteBeforePause(t *testing.T) {
	oldStdin := stdin
	oldRaw := rawInputEnabled
	defer func() {
		stdin = oldStdin
		rawInputEnabled = oldRaw
	}()
	// Command at the `[guide] $` prompt, then an answer to the check, then
	// Enter to clear the advance pause.
	stdin = bufio.NewReader(strings.NewReader("echo USE\n1\n\n"))
	rawInputEnabled = func() bool { return false }

	step := orderingGuideStep("Teaching note body for the ordering test.")

	var answered int
	var ok bool
	out := captureStdout(func() {
		_, answered, ok = runGuideStep(&Session{System: SystemInfo{NumCPU: 1}}, step, false)
	})

	if !ok {
		t.Fatalf("runGuideStep returned ok=false, want true:\n%s", out)
	}
	if answered != 1 {
		t.Fatalf("answered = %d, want 1:\n%s", answered, out)
	}

	feedbackAt := strings.Index(out, "--- Feedback ---")
	teachingAt := strings.Index(out, "--- Teaching note ---")
	pauseAt := strings.Index(out, "Press Enter to continue...")
	if feedbackAt < 0 || teachingAt < 0 || pauseAt < 0 {
		t.Fatalf("expected feedback, teaching note, and pause all present:\n%s", out)
	}
	if !(feedbackAt < teachingAt && teachingAt < pauseAt) {
		t.Fatalf("expected order feedback(%d) < teaching(%d) < pause(%d):\n%s",
			feedbackAt, teachingAt, pauseAt, out)
	}
}

func TestGuideFinalStepDefersPauseToSummary(t *testing.T) {
	oldStdin := stdin
	oldRaw := rawInputEnabled
	defer func() {
		stdin = oldStdin
		rawInputEnabled = oldRaw
	}()
	// No trailing Enter: the final step must not run a per-step pause (that
	// pause belongs to finishGuide), so no line is consumed for one.
	stdin = bufio.NewReader(strings.NewReader("echo USE\n1\n"))
	rawInputEnabled = func() bool { return false }

	step := orderingGuideStep("Final-step teaching note.")

	var ok bool
	out := captureStdout(func() {
		_, _, ok = runGuideStep(&Session{System: SystemInfo{NumCPU: 1}}, step, true)
	})

	if !ok {
		t.Fatalf("runGuideStep returned ok=false, want true:\n%s", out)
	}
	if !strings.Contains(out, "--- Teaching note ---") {
		t.Fatalf("expected the teaching note on the final step:\n%s", out)
	}
	if strings.Contains(out, "Press Enter to continue...") {
		t.Fatalf("final step should defer its pause to finishGuide, but paused:\n%s", out)
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

func mustFindQuestion(t *testing.T, qs []Question, fragment string) Question {
	t.Helper()
	q, ok := findQuestionByStemFragment(qs, fragment)
	if !ok {
		t.Fatalf("expected a question whose stem contains %q; got %v", fragment, stems(qs))
	}
	return q
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
