package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"unsafe"
)

const (
	appName = "use-tool"
)

const (
	maxCaptureBytes  = 1 << 20 // per-command output cap (1 MB)
	maxCapturedItems = 50      // per-session command-history cap
)

var stdin = bufio.NewReader(os.Stdin)
var rawInputEnabled = stdinIsTerminal

type terminalKey int

const (
	keyUnknown terminalKey = iota
	keyEnter
	keyUp
	keyDown
	keySpace
	keyQuit
	keyDigit
	keyClear
	keySeparator
)

type keyEvent struct {
	Key   terminalKey
	Digit int
}

func main() {
	if len(os.Args) < 2 {
		usage(2)
	}
	exitOnSigint()

	switch os.Args[1] {
	case "guide":
		cmdGuide(os.Args[2:])
	case "practice":
		cmdPractice(os.Args[2:])
	case "commands":
		cmdCommands(os.Args[2:])
	case "list":
		cmdList()
	case "version", "--version", "-v":
		fmt.Printf("%s %s\n", appName, currentVersion())
	case "help", "--help", "-h":
		usage(0)
	default:
		unknownCommand(os.Args[1])
	}
}

var topLevelCommands = []string{"guide", "practice", "commands", "list", "version", "help"}

func unknownCommand(name string) {
	fmt.Fprintf(os.Stderr, "unknown command %q", name)
	if suggestion, ok := suggestCommand(name, topLevelCommands); ok {
		fmt.Fprintf(os.Stderr, "; did you mean %q?", suggestion)
	}
	fmt.Fprintln(os.Stderr)
	usage(2)
}

func usage(code int) {
	out := os.Stdout
	if code != 0 {
		out = os.Stderr
	}
	fmt.Fprintf(out, `%s %s — practice the USE method on a live Linux system

New here? Run: use-tool guide cpu

Usage:
  use-tool guide <resource>     Walk through the USE method as a guided checklist
  use-tool practice <resource>  Free-form investigation, then understanding check
  use-tool commands <resource>  Print a reference of relevant commands and what they show
  use-tool list                 Show available resources
  use-tool version              Print version
  use-tool help                 This message

Available resources: %s
`, appName, currentVersion(), strings.Join(resourceNames(), ", "))
	os.Exit(code)
}

func cmdList() {
	for _, name := range resourceNames() {
		fmt.Printf("  %-10s  %s\n", name, investigations[name].Title)
	}
}

func cmdCommands(args []string) {
	if len(args) < 1 {
		fmt.Fprintln(os.Stderr, "usage: use-tool commands <resource>")
		os.Exit(2)
	}
	inv, err := getInvestigation(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	printCommands(inv, detectSystem())
}

type CapturedCommand struct {
	Cmd      string
	Output   string
	Failed   bool
	ExitCode int
}

type Session struct {
	Investigation *Investigation
	System        SystemInfo
	Captured      []CapturedCommand
	// kernelLogBlocks counts how many times this session has seen a
	// permission-blocked dmesg or journalctl call. After the second strike
	// we emit a one-shot note that the host is hiding kernel logs across
	// the board, so the learner stops chasing the other tool.
	kernelLogBlocks     int
	kernelLogBlockNoted bool
}

type SystemInfo struct {
	NumCPU        int
	HasMpstat     bool
	HasPidstat    bool
	HasSar        bool
	HasPSI        bool
	HasJournalctl bool
}

func detectSystem() SystemInfo {
	return SystemInfo{
		NumCPU:        runtime.NumCPU(),
		HasMpstat:     haveCmd("mpstat"),
		HasPidstat:    haveCmd("pidstat"),
		HasSar:        haveCmd("sar"),
		HasPSI:        fileExists("/proc/pressure/cpu"),
		HasJournalctl: haveCmd("journalctl"),
	}
}

func haveCmd(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runCommand(cmdStr string) CapturedCommand {
	return runCommandStreaming(cmdStr, os.Stdout)
}

// runCommandStreaming runs cmdStr, capturing stdout+stderr while mirroring
// stdout to liveOut. Pass os.Stdout for normal live display, or io.Discard
// when the caller intends to post-process the output before showing it.
// Stderr always streams to os.Stderr so genuine errors surface immediately.
func runCommandStreaming(cmdStr string, liveOut io.Writer) CapturedCommand {
	cmd := exec.Command("sh", "-c", cmdStr)
	buf := &cappedBuffer{limit: maxCaptureBytes}
	cmd.Stdout = io.MultiWriter(liveOut, buf)
	cmd.Stderr = io.MultiWriter(os.Stderr, buf)
	cmd.Stdin = os.Stdin
	failed := false
	exitCode := 0
	if err := cmd.Run(); err != nil {
		failed = true
		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			fmt.Fprintln(os.Stderr)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() >= 0 {
			exitCode = exitErr.ExitCode()
			if isNoMatchGrepExit(cmdStr, exitCode, buf.String()) {
				failed = false
				fmt.Fprintln(os.Stderr, "[no matching lines]")
			} else {
				fmt.Fprintf(os.Stderr, "[command exited with status %d]\n", exitCode)
			}
		} else {
			exitCode = -1
			fmt.Fprintf(os.Stderr, "[command failed: %v]\n", err)
		}
	}
	if isDmesgPermissionFailure(buf.String()) {
		if !failed {
			failed = true
			exitCode = -1
		}
		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprintf(os.Stderr, "[command failed: %s]\n", dmesgPermissionFailureMessage(haveCmd("journalctl")))
	}
	if isJournalctlFailure(cmdStr, buf.String()) {
		if !failed {
			failed = true
			exitCode = -1
		}
		if buf.Len() > 0 && !bytes.HasSuffix(buf.Bytes(), []byte("\n")) {
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprintln(os.Stderr, "[command failed: journalctl could not read the kernel log; try dmesg or sudo dmesg]")
	}
	return CapturedCommand{Cmd: cmdStr, Output: buf.String(), Failed: failed, ExitCode: exitCode}
}

func isNoMatchGrepExit(cmdStr string, exitCode int, output string) bool {
	return exitCode == 1 && lastPipelineCommandBase(cmdStr) == "grep" && strings.TrimSpace(output) == ""
}

func lastPipelineCommandBase(cmdStr string) string {
	fields := strings.Fields(lastPipelineSegment(cmdStr))
	if len(fields) == 0 {
		return ""
	}
	if fields[0] == "sudo" && len(fields) > 1 {
		return fields[1]
	}
	return fields[0]
}

func lastPipelineSegment(cmdStr string) string {
	start := 0
	inSingle := false
	inDouble := false
	escaped := false
	for i, r := range cmdStr {
		if escaped {
			escaped = false
			continue
		}
		if r == '\\' && !inSingle {
			escaped = true
			continue
		}
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '|':
			if !inSingle && !inDouble {
				start = i + len("|")
			}
		}
	}
	return strings.TrimSpace(cmdStr[start:])
}

func (s *Session) runAndCapture(cmdStr string) CapturedCommand {
	return s.runAndCaptureFiltered(cmdStr, nil)
}

// runAndCaptureFiltered runs cmdStr and, when filter is non-nil, rewrites the
// captured output before it is shown or stored. To avoid the raw output
// streaming past first, the command runs without live stdout and the filtered
// result is printed afterward.
func (s *Session) runAndCaptureFiltered(cmdStr string, filter func(CapturedCommand) string) CapturedCommand {
	var c CapturedCommand
	if filter == nil {
		c = runCommand(cmdStr)
	} else {
		c = runCommandStreaming(cmdStr, io.Discard)
	}
	if c.Failed {
		if isDmesgPermissionFailure(c.Output) || isJournalctlFailure(c.Cmd, c.Output) {
			s.kernelLogBlocks++
			if s.kernelLogBlocks >= 2 && !s.kernelLogBlockNoted {
				s.kernelLogBlockNoted = true
				fmt.Fprintln(os.Stderr, "[note: both dmesg and journalctl -k are blocked for this user on this host")
				fmt.Fprintln(os.Stderr, "       (usually kernel.dmesg_restrict=1). The 'Errors' leg of USE")
				fmt.Fprintln(os.Stderr, "       can't be checked without permissions here — `skip` and move on,")
				fmt.Fprintln(os.Stderr, "       or run it again with sudo if you need the kernel log.]")
			}
		}
		return c
	}
	if filter != nil {
		c.Output = filter(c)
		fmt.Print(c.Output)
		if c.Output != "" && !strings.HasSuffix(c.Output, "\n") {
			fmt.Println()
		}
	}
	s.appendCaptured(c)
	return c
}

func isDmesgPermissionFailure(output string) bool {
	low := strings.ToLower(output)
	return strings.Contains(low, "dmesg: read kernel buffer failed") &&
		strings.Contains(low, "operation not permitted")
}

func dmesgPermissionFailureMessage(hasJournalctl bool) string {
	if hasJournalctl {
		return "dmesg could not read the kernel buffer; try again with sudo or journalctl -k"
	}
	return "dmesg could not read the kernel buffer; try again with sudo"
}

func isJournalctlFailure(cmdStr, output string) bool {
	if !strings.Contains(cmdStr, "journalctl") {
		return false
	}
	low := strings.ToLower(output)
	for _, marker := range []string{
		"no journal files were opened due to insufficient permissions",
		"failed to open journal",
		"failed to open files",
		"failed to connect to bus",
		"failed to get journal",
	} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

// cappedBuffer accepts unlimited writes (so the user still sees full output on
// stdout via MultiWriter) but only retains the first `limit` bytes for capture.
type cappedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	remaining := c.limit - c.buf.Len()
	if remaining <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > remaining {
		c.buf.Write(p[:remaining])
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (c *cappedBuffer) String() string {
	if c.truncated {
		return c.buf.String() + "\n[output truncated at 1 MB]\n"
	}
	return c.buf.String()
}

func (c *cappedBuffer) Len() int {
	return c.buf.Len()
}

func (c *cappedBuffer) Bytes() []byte {
	return c.buf.Bytes()
}

// appendCaptured records a command in the session, dropping the oldest entry
// (with a one-line note) once the history cap is reached.
func (s *Session) appendCaptured(c CapturedCommand) {
	if len(s.Captured) >= maxCapturedItems {
		dropped := s.Captured[0].Cmd
		s.Captured = append(s.Captured[:0], s.Captured[1:]...)
		fmt.Fprintf(os.Stderr, "(history cap reached; dropped oldest: %q)\n", dropped)
	}
	s.Captured = append(s.Captured, c)
}

func readLine() (string, bool) {
	line, err := stdin.ReadString('\n')
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(line), true
}

func enterRawInput() (restore func(), ok bool) {
	if !rawInputEnabled() {
		return nil, false
	}
	var old syscall.Termios
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		os.Stdin.Fd(),
		uintptr(syscall.TCGETS),
		uintptr(unsafe.Pointer(&old)),
	)
	if errno != 0 {
		return nil, false
	}
	raw := old
	raw.Iflag &^= syscall.ICRNL | syscall.IXON
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.ISIG
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	_, _, errno = syscall.Syscall(
		syscall.SYS_IOCTL,
		os.Stdin.Fd(),
		uintptr(syscall.TCSETS),
		uintptr(unsafe.Pointer(&raw)),
	)
	if errno != 0 {
		return nil, false
	}
	return func() {
		syscall.Syscall(
			syscall.SYS_IOCTL,
			os.Stdin.Fd(),
			uintptr(syscall.TCSETS),
			uintptr(unsafe.Pointer(&old)),
		)
	}, true
}

func readTerminalKey() (keyEvent, error) {
	b, err := stdin.ReadByte()
	if err != nil {
		return keyEvent{}, err
	}
	switch {
	case b == '\r' || b == '\n':
		return keyEvent{Key: keyEnter}, nil
	case b == ' ':
		return keyEvent{Key: keySpace}, nil
	case b == 'q' || b == 'Q' || b == 3 || b == 4:
		return keyEvent{Key: keyQuit}, nil
	case b == 'n' || b == 'N':
		return keyEvent{Key: keyClear}, nil
	case b == ',':
		return keyEvent{Key: keySeparator}, nil
	case b >= '1' && b <= '9':
		return keyEvent{Key: keyDigit, Digit: int(b - '0')}, nil
	case b == 0x1b:
		second, err := stdin.ReadByte()
		if err != nil {
			return keyEvent{Key: keyQuit}, nil
		}
		if second != '[' {
			return keyEvent{Key: keyUnknown}, nil
		}
		third, err := stdin.ReadByte()
		if err != nil {
			return keyEvent{Key: keyUnknown}, nil
		}
		switch third {
		case 'A':
			return keyEvent{Key: keyUp}, nil
		case 'B':
			return keyEvent{Key: keyDown}, nil
		}
	}
	return keyEvent{Key: keyUnknown}, nil
}

func stripCopiedShellPrompt(line string) (string, bool) {
	line = strings.TrimSpace(line)
	if line == "$" {
		return "", true
	}
	if strings.HasPrefix(line, "$ ") {
		return strings.TrimSpace(strings.TrimPrefix(line, "$")), true
	}
	return line, false
}

func exitOnSigint() {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGINT)
	go func() {
		for range ch {
			fmt.Fprintln(os.Stderr, "\nInterrupted. Exiting.")
			os.Exit(130)
		}
	}()
}

func requireInteractive(mode string) {
	if stdinIsTerminal() {
		return
	}
	fmt.Fprintf(os.Stderr, "use-tool %s requires an interactive terminal on stdin.\n", mode)
	fmt.Fprintln(os.Stderr, "Run `use-tool commands cpu` for a non-interactive command reference.")
	os.Exit(2)
}

func stdinIsTerminal() bool {
	var termios syscall.Termios
	_, _, errno := syscall.Syscall(
		syscall.SYS_IOCTL,
		os.Stdin.Fd(),
		uintptr(syscall.TCGETS),
		uintptr(unsafe.Pointer(&termios)),
	)
	return errno == 0
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func suggestCommand(input string, candidates []string) (string, bool) {
	best := ""
	bestDistance := 0
	for _, candidate := range candidates {
		dist := levenshtein(input, candidate)
		if best == "" || dist < bestDistance {
			best = candidate
			bestDistance = dist
		}
	}
	if best == "" || bestDistance > 2 {
		return "", false
	}
	return best, true
}

func levenshtein(a, b string) int {
	if a == b {
		return 0
	}
	if len(a) == 0 {
		return len(b)
	}
	if len(b) == 0 {
		return len(a)
	}

	prev := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = minInt(
				cur[j-1]+1,
				prev[j]+1,
				prev[j-1]+cost,
			)
		}
		prev = cur
	}
	return prev[len(b)]
}

func minInt(xs ...int) int {
	m := xs[0]
	for _, x := range xs[1:] {
		if x < m {
			m = x
		}
	}
	return m
}
